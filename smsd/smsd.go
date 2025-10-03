package smsd

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ctaoist/gammu-web/config"
	"github.com/ctaoist/gammu-web/db"
	"github.com/ctaoist/gammu-web/message"

	log "github.com/sirupsen/logrus"
)

type event struct{}

type Msg = message.Msg

var (
	GSM_StateMachine       *StateMachine
	sendMsgEvent           = make(chan event, 1000)
	boxEvent               = make(chan int, 1000) // 1: receive, 2: send
	receiveLoopSleep       = 1
	outBox, inBox, sentBox []Msg
	inLock, outLock        sync.RWMutex
	ownNumber              = ""
	errCount               = 0
	lastErr                error
	forwardSvc             *ForwardConn
	shutdownChan           = make(chan struct{})
	wg                     sync.WaitGroup
	callCache              = make(map[string]time.Time) // 用于去重的缓存
	callCacheLock          sync.RWMutex
)

type SMSReadError struct {
	Count     int
	LastError error
	LastTime  time.Time
}

var smsReadError SMSReadError

// 改进的错误计数器，专门处理短信读取错误
func smsErrorCounter(e error) {
	now := time.Now()

	// 如果距离上次错误超过1分钟，重置计数器
	if now.Sub(smsReadError.LastTime) > time.Minute {
		smsReadError.Count = 0
		smsReadError.LastError = nil
	}

	if smsReadError.LastError != nil && smsReadError.LastError.Error() == e.Error() {
		smsReadError.Count++
	} else {
		smsReadError.LastError = e
		smsReadError.Count = 1
	}
	smsReadError.LastTime = now

	log.Warnf("短信读取错误计数: %d/3, 错误: %v", smsReadError.Count, e)

	// 连续3次错误，执行重置
	if smsReadError.Count >= 3 {
		log.Warn("检测到连续3次短信读取失败，执行连接重置...")
		if resetErr := resetGSMConnection(); resetErr != nil {
			log.Errorf("连接重置失败: %v", resetErr)
		} else {
			log.Info("连接重置成功，重置错误计数器")
			smsReadError.Count = 0
			smsReadError.LastError = nil
		}
	}
}

// 重置GSM连接的统一函数
func resetGSMConnection() error {
	if GSM_StateMachine == nil {
		return fmt.Errorf("GSM状态机未初始化")
	}

	log.Info("开始重置GSM连接...")

	// 方法1: 软重置
	if err := GSM_StateMachine.Reset(); err != nil {
		log.Warnf("软重置失败: %v，尝试硬重置", err)

		// 方法2: 硬重置
		if err := GSM_StateMachine.HardReset(); err != nil {
			log.Warnf("硬重置失败: %v，尝试重新连接", err)

			// 方法3: 重新连接
			if err := GSM_StateMachine.Reconnect(); err != nil {
				log.Errorf("重新连接失败: %v", err)
				return err
			}
		}
	}

	// 重置后等待设备稳定
	time.Sleep(3 * time.Second)
	log.Info("GSM连接重置完成")
	return nil
}

func errCounter(e error) {
	if lastErr != e {
		lastErr = e
		errCount = 1
		return
	}
	errCount++
	if errCount >= 3 {
		log.Warn("GSMReset errCount >= 3, GSM hard reset")
		GSM_StateMachine.HardReset()
		errCount = 0 // 重置计数器
	}
}

func Init(config string) {
	var e error
	if config[:2] == "~/" {
		u, _ := user.Current()
		config = u.HomeDir + "/" + config[1:]
	}

	GSM_StateMachine, e = NewStateMachine(config)
	if e != nil {
		log.Fatalf("GammuInit %v", e)
	}

	if e := GSM_StateMachine.Connect(); e != nil {
		log.Fatalf("GammuInit %v", e)
	}

	log.Infof("GammuGetCountryCode Country code of phone: %s", GSM_StateMachine.GetCountryCode())

	// 获取并验证本机号码
	ownNumber = GSM_StateMachine.GetOwnNumber()
	if normalized, err := NormalizePhoneNumber(ownNumber); err != nil {
		log.Errorf("本机号码格式无效: %s, 错误: %v", ownNumber, err)
		// 可以设置一个默认值或尝试修复
		GSM_StateMachine.number = "+8618611500513" // 使用示例号码
	} else {
		GSM_StateMachine.number = normalized
	}

	log.Infof("GammuGetOwnNumber Own phone number: %s", GSM_StateMachine.GetOwnNumber())
	forwardSvc = initForward()

	// 启动工作协程
	wg.Add(3)
	go ReseiveSendLoop()
	go StorageSMSLoop()
	go CallMonitorLoop() // 事件驱动的通话监控

	// 设置信号处理，优雅关闭
	setupSignalHandler()
}

func setupSignalHandler() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Infof("收到信号 %v，开始优雅关闭...", sig)
		Close()
	}()
}

// 修改 Close 函数，确保正确关闭所有通道
func Close() {
	log.Info("开始关闭短信服务...")

	// 关闭事件通道，停止新的处理
	close(shutdownChan)

	// 等待所有工作协程完成
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// 带超时的等待
	select {
	case <-done:
		log.Info("所有工作协程已退出")
	case <-time.After(30 * time.Second):
		log.Warn("关闭超时，强制退出")
	}

	// 关闭转发服务
	if forwardSvc != nil {
		forwardSvc.Close()
	}

	// 关闭GSM状态机
	if GSM_StateMachine != nil {
		GSM_StateMachine.free()
	}

	log.Info("短信服务已关闭")
}

func GetOwnNumber() string {
	if !config.TestMode {
		return GSM_StateMachine.GetOwnNumber()
	}
	if ownNumber == "" {
		ownNumber = db.GetFirstCardNumber()
	}
	return ownNumber
}

func SendSMS(phone_number, text string) error {
	if phone_number == "" || text == "" {
		return fmt.Errorf("手机号或短信内容不能为空")
	}

	// 使用统一的号码格式化函数
	formattedNumber := AddCountryCode(phone_number)

	t := time.Now()
	msg := Msg{"", GSM_StateMachine.GetOwnNumber(), formattedNumber, text, true, t}
	msg.GenerateId()

	outLock.Lock()
	outBox = append(outBox, msg)
	outLock.Unlock()

	select {
	case sendMsgEvent <- event{}:
		log.Infof("短信已加入发送队列: %s -> %s", msg.SelfNumber, msg.Number)
		return nil
	case <-shutdownChan:
		return fmt.Errorf("服务正在关闭，无法发送短信")
	default:
		return fmt.Errorf("发送队列已满，请稍后重试")
	}
}

func ReceiveSMS() error {
	sms, err := GSM_StateMachine.GetSMS()
	if err != nil {
		if err == io.EOF {
			return io.EOF
		}
		log.Errorf("读取短信失败: %v", err)

		// 增加错误恢复机制
		if strings.Contains(err.Error(), "segment") ||
			strings.Contains(err.Error(), "memory") ||
			strings.Contains(err.Error(), "invalid") {
			log.Warn("检测到内存相关错误，尝试重置GSM状态...")
			if resetErr := GSM_StateMachine.ResetSMSStatus(); resetErr != nil {
				log.Errorf("重置GSM状态失败: %v", resetErr)
			} else {
				log.Info("GSM状态重置成功")
			}
		}

		smsErrorCounter(err)
		return err
	}

	if len(strings.TrimSpace(sms.Body)) == 0 {
		log.Warn("收到空短信内容，跳过处理")
		return nil
	}

	log.Debugf("收到短信 - 发件人: %s, 时间: %s, 内容长度: %d",
		sms.Number, sms.Time.Format(time.RFC3339), len(sms.Body))

	if sms.Report {
		m := strings.TrimSpace(sms.Body)
		if strings.ToLower(m) == "delivered" {
			log.Debugf("短信送达报告: %+v", sms)
			// boxEvent <- 2
		}
	} else {
		msg := Msg{
			ID:         "",
			SelfNumber: GSM_StateMachine.GetOwnNumber(),
			Number:     sms.Number,
			Text:       sms.Body,
			Sent:       false,
			Time:       sms.Time,
		}
		msg.GenerateId()
		log.Infof("收到短信 - 发件人: %s, 内容: %s", msg.Number, msg.Text)

		inLock.Lock()
		inBox = append(inBox, msg)
		inLock.Unlock()

		select {
		case boxEvent <- 1:
			// 成功发送事件
		case <-shutdownChan:
			log.Warn("服务正在关闭，丢弃收到的短信")
		default:
			log.Warn("事件队列已满，丢弃收到的短信")
		}
	}
	return nil
}

func ReseiveSendLoop() {
	defer wg.Done()

	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-shutdownChan:
			log.Info("接收发送循环收到关闭信号，退出")
			return
		case <-sendMsgEvent:
			// 处理发送事件
			t := time.Now()
			outLock.Lock()
			i := 0
			for _, m := range outBox {
				if err := GSM_StateMachine.SendLongSMS(m.Number, m.Text, true); err != nil {
					log.Errorf("发送短信失败: %v", err)
					continue
				}
				// 发送成功立即入库
				db.InsertSMS(m)
				db.UpdateAbstract(m)

				// 加入 sentBox 用于 WebSocket 推送
				sentBox = append(sentBox, m)

				i++
				if time.Since(t) > 3*time.Second {
					break
				}
			}
			sentBox = outBox[:i]
			outBox = outBox[i:]
			outLock.Unlock()
			// 触发 WebSocket 推送
			if len(sentBox) > 0 {
				boxEvent <- 2
			}
		default:
			// 没有发送请求，尝试接收短信
			err := ReceiveSMS()
			if err == nil {
				// 成功读取到短信或没有错误，重置 backoff
				backoff = 1 * time.Second
			} else if err == io.EOF {
				// 没有短信：等一会再尝试
				select {
				case <-time.After(backoff):
				case <-shutdownChan:
					log.Info("接收发送循环收到关闭信号，退出")
					return
				}
				if backoff < maxBackoff {
					backoff *= 2
				}
			} else {
				// 非 EOF 错误
				log.Errorf("ReceiveSMS error: %v", err)
				select {
				case <-time.After(backoff):
				case <-shutdownChan:
					log.Info("接收发送循环收到关闭信号，退出")
					return
				}
				if backoff < maxBackoff {
					backoff *= 2
				}
			}
		}

		// 定期短暂停顿，避免忙循环
		time.Sleep(time.Duration(receiveLoopSleep) * time.Second)
	}
}

func StorageSMSLoop() {
	defer wg.Done()

	number := GSM_StateMachine.GetOwnNumber()

	for {
		select {
		case <-shutdownChan:
			log.Info("存储循环收到关闭信号，退出")
			// 确保程序退出时关闭转发服务
			if forwardSvc != nil {
				forwardSvc.Close()
			}
			return

		case i := <-boxEvent:
			if i == 1 { // receive sms
				inLock.Lock()
				if len(inBox) == 0 {
					inLock.Unlock()
					continue
				}

				// 深度复制数据，避免数据竞争
				msgsToForward := make([]Msg, len(inBox))
				copy(msgsToForward, inBox)

				// 发送 WebSocket 通知
				for _, msg := range inBox {
					message.WsSendSMS(number, msg)
				}

				// 存储到数据库
				db.InsertSMSMany(inBox)
				db.UpdateAbstract(inBox[len(inBox)-1])
				// 转发短信
				forwardSvc.PutMsgs(msgsToForward)

				inBox = []Msg{}
				inLock.Unlock()

			} else if i == 2 { // send sms
				outLock.Lock()
				if len(sentBox) == 0 {
					outLock.Unlock()
					continue
				}

				for _, msg := range sentBox {
					log.Infof("SentSMS To %s with text: %s", msg.Number, msg.Text)
					message.WsSendSMS(number, msg)
				}
				// db.InsertSMSMany(sentBox)
				// db.UpdateAbstract(sentBox[len(sentBox)-1])
				sentBox = []Msg{}
				outLock.Unlock()
			}
		}
	}
}

// GetStats 获取服务统计信息（用于监控）
func GetStats() map[string]interface{} {
	inLock.RLock()
	outLock.RLock()
	defer inLock.RUnlock()
	defer outLock.RUnlock()

	return map[string]interface{}{
		"inbox_count":   len(inBox),
		"outbox_count":  len(outBox),
		"sentbox_count": len(sentBox),
		"err_count":     errCount,
	}
}

// CallMonitorLoop 通话记录监控循环（事件驱动）
func CallMonitorLoop() {
	defer wg.Done()

	log.Info("通话记录监控循环启动（事件驱动）")

	for {
		select {
		case <-shutdownChan:
			log.Info("通话记录监控循环收到关闭信号，退出")
			return
		case ev := <-callEvents:
			// 只处理来电（incoming）
			if ev.Type != "incoming" {
				// 如果你也想转发 missed/outgoing，可在此添加逻辑
				continue
			}

			// 生成唯一标识（号码+时间戳）
			callKey := fmt.Sprintf("%s_%d", ev.Number, ev.Time.Unix())

			callCacheLock.RLock()
			_, exists := callCache[callKey]
			callCacheLock.RUnlock()

			if exists {
				// 已处理过，跳过
				continue
			}

			// 标记为已处理
			callCacheLock.Lock()
			callCache[callKey] = ev.Time
			callCacheLock.Unlock()

			// 记录并转发
			log.Infof("转发来电记录: %s (%s) - %s", ev.Number, ev.Name, ev.Time.Format("2006-01-02 15:04:05"))
			if forwardSvc != nil {
				forwardSvc.PutCall(ev)
			}
		case <-time.After(30 * time.Second):
			// 周期性清理缓存（避免无限增长）
			cleanupCallCache()
		}
	}
}

// cleanupCallCache 清理通话记录缓存
func cleanupCallCache() {
	callCacheLock.Lock()
	defer callCacheLock.Unlock()

	cutoff := time.Now().Add(-24 * time.Hour)
	for key, timestamp := range callCache {
		if timestamp.Before(cutoff) {
			delete(callCache, key)
		}
	}
}
