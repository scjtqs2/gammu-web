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
)

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
	log.Infof("GammuGetOwnNumber Own phone number: %s", GSM_StateMachine.GetOwnNumber())

	forwardSvc = initForward()

	// 启动工作协程
	wg.Add(2)
	go ReseiveSendLoop()
	go StorageSMSLoop()

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

	if phone_number[0] != '+' {
		phone_number = GSM_StateMachine.country + phone_number
	}

	t := time.Now()
	msg := Msg{"", GSM_StateMachine.GetOwnNumber(), phone_number, text, true, t}
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
			// 没有短信 —— 正常，返回 EOF 由上层循环决定是否重连/等待
			return io.EOF
		}
		log.Errorf("读取短信失败: %v", err)
		errCounter(err)
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
			boxEvent <- 2
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
				i++
				if time.Since(t) > 3*time.Second {
					break
				}
			}
			sentBox = outBox[:i]
			outBox = outBox[i:]
			outLock.Unlock()

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
				db.InsertSMSMany(sentBox)
				db.UpdateAbstract(sentBox[len(sentBox)-1])
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
