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
			// 没有短信是正常情况，不需要记录错误
			return err
		}
		log.Errorf("读取短信失败: %v", err)
		errCounter(err)
		return err
	}

	if len(sms.Body) <= 0 {
		log.Warn("收到空短信内容，跳过处理")
		// 删除空短信
		c_gsm_deleteSMS(GSM_StateMachine, lastSms)
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
		// 保存到收件箱
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
	for {
		err := ReceiveSMS()
		if err == io.EOF {
			GSM_StateMachine.Reconnect()
			time.Sleep(time.Duration(receiveLoopSleep) * time.Second)
			continue
		}
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
		case <-time.After(time.Duration(receiveLoopSleep) * time.Second):
		}
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
