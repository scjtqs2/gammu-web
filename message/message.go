package message

import (
	"encoding/json"
	"math/rand"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

type Msg struct {
	ID         string    `json:"id"`
	SelfNumber string    `json:"self_number"`
	Number     string    `json:"number"`
	Text       string    `json:"text"`
	Sent       bool      `json:"sent"` // the msg is sent or recieved
	Time       time.Time `json:"time"`
}

type WsMsg struct {
	// Type string `json:"type"`
	Msg Msg `json:"msg"`
}

type Websocket struct {
	Id   string
	Conn *websocket.Conn
}

var (
	WS          = map[string][]Websocket{} // {"own_phone_number": [...]}
	heart       = `{"type": "heartbeat"}`
	letterRunes = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
)

func (m *Msg) GenerateId() {
	m.ID = strconv.FormatInt(m.Time.UnixMilli(), 10) + randomStr(5)
}

func (m *Msg) GenerateIdA() {
	m.ID = m.SelfNumber + "_" + m.Number
}

func NewWebSocket(id string, conn *websocket.Conn) Websocket {
	return Websocket{id, conn}
}

func RemoveWs(number, id string) {
	for i, w := range WS[number] {
		if w.Id == id {
			WS[number] = append(WS[number][:i], WS[number][i+1:]...)
			return
		}
	}
}

func HeartBeatLoop() {
	for {
		time.Sleep(5 * time.Second)
		if len(WS) <= 0 {
			continue
		}
		for _, v := range WS {
			// log.Debug("WsHeart", number, " has ", len(v), " websocket connection(s)")
			for _, ws := range v {
				if e := ws.Conn.WriteMessage(websocket.TextMessage, []byte(heart)); e != nil {
					log.Errorf("WsHeartBeat %v", e)
				}
			}
		}
	}
}

func WsSendSMS(number string, msg Msg) {
	// 创建一个临时结构体来处理序列化
	type TempMsg struct {
		ID         string `json:"id"`
		SelfNumber string `json:"self_number"`
		Number     string `json:"number"`
		Text       string `json:"text"`
		Sent       bool   `json:"sent"`
		Time       string `json:"time"` // 使用字符串格式
	}

	type TempWsMsg struct {
		Msg TempMsg `json:"msg"`
	}

	// 转换时间格式
	tempMsg := TempMsg{
		ID:         msg.ID,
		SelfNumber: msg.SelfNumber,
		Number:     msg.Number,
		Text:       msg.Text,
		Sent:       msg.Sent,
		Time:       msg.Time.Format(time.RFC3339), // 或者使用其他格式
	}

	m := TempWsMsg{tempMsg}
	// m := WsMsg{msg}
	b, err := json.Marshal(&m)
	if err != nil {
		log.Errorf("JsonMarshal %v", err)
		return
	}
	for _, w := range WS[number] {
		err := w.Conn.WriteMessage(websocket.TextMessage, b)
		if err != nil {
			log.Error("WsWrite", err)
			break
		}
	}
}

func randomStr(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

func GenerateId() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 10) + randomStr(5)
}
