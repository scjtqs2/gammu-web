package smsd

import (
	"io"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/ctaoist/gammu-web/config"
	"github.com/ctaoist/gammu-web/db"
	"github.com/ctaoist/gammu-web/message"

	log "github.com/sirupsen/logrus"
)

type event struct {
}

type Msg = message.Msg

var (
	GSM_StateMachine       *StateMachine
	sendMsgEvent           = make(chan event, 1000)
	boxEvent               = make(chan int, 1000) // 1: reveive, 2: send
	receiveLoopSleep       = 1
	outBox, inBox, sentBox []Msg
	inLock, outLock        sync.Mutex
	ownNumber              = ""
	errCount               = 0
	lastErr                error
	forwardSvc             *ForwardConn
)

func errCounter(e error) {
	if lastErr != e {
		lastErr = e
		errCount = 1
		return
	}
	errCount = errCount + 1
	if errCount >= 3 {
		log.Warn("GSMReset errCount >= 3, GSM hard reset")
		GSM_StateMachine.HardReset()
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
	go ReseiveSendLoop()
	go StorageSMSLoop()
}

func Close() {
	GSM_StateMachine.free()
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

func SendSMS(phone_number, text string) {
	if phone_number[0] != '+' {
		phone_number = GSM_StateMachine.country + phone_number
	}
	t := time.Now()
	msg := Msg{"", GSM_StateMachine.GetOwnNumber(), phone_number, text, true, t}
	msg.GenerateId()
	outLock.Lock()
	outBox = append(outBox, msg)
	outLock.Unlock()
	sendMsgEvent <- event{}
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
		// 如果你已经在 GetSMS 里删除了短信，这里不必再删除。
		// 但若你使用 GSM_GetNextSMS(..., C.FALSE) 则需要显式删除，这里可以用 c_gsm_deleteSMS
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
		boxEvent <- 1
	}
	return nil
}

func ReseiveSendLoop() {
	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second

	for {
		// 先处理发送事件优先级（非阻塞）
		select {
		case <-sendMsgEvent:
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
			// 没有发送请求，继续接收流程
		}

		// 尝试接收一条短信
		err := ReceiveSMS()
		if err == nil {
			// 成功读取到短信或没有错误，重置 backoff
			backoff = 1 * time.Second
		} else if err == io.EOF {
			// 没有短信：等一会再尝试（不立即重连，避免频繁重连）
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		} else {
			// 非 EOF 错误：计数器会处理是否重置设备
			log.Errorf("ReceiveSMS error: %v", err)
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
			}
			// 如果 errCounter 触发了 GSM_Reset/HadReset，Reconnect 会在其它流程里触发或你可以在这里调用
			continue
		}

		// 定期短暂停顿，避免忙循环
		time.Sleep(time.Duration(receiveLoopSleep) * time.Second)
	}
}

func StorageSMSLoop() {
	number := GSM_StateMachine.GetOwnNumber()
	// 确保程序退出时关闭转发服务
	defer func() {
		if forwardSvc != nil {
			forwardSvc.Close()
		}
	}()

	for {
		i := <-boxEvent
		if i == 1 { // receive sms
			if len(inBox) < 1 {
				continue
			}
			inLock.Lock()
			// 深度复制数据，避免数据竞争
			msgsToForward := make([]Msg, len(inBox))
			copy(msgsToForward, inBox)

			for _, msg := range inBox {
				message.WsSendSMS(number, msg)
			}
			db.InsertSMSMany(inBox)
			db.UpdateAbstract(inBox[len(inBox)-1])
			// 如果开启了短信转发，则将短信转发给forward接收端。
			forwardSvc.PutMsgs(msgsToForward)

			inBox = []Msg{}
			inLock.Unlock()
		} else if i == 2 { // send sms
			if len(sentBox) < 1 {
				continue
			}
			outLock.Lock()
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
