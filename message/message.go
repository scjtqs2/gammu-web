package message

import (
	"encoding/json"
	"math/rand"
	"strconv"
	"sync"
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
	Mu   sync.Mutex // 为每个连接添加互斥锁
}

var (
	WS          = map[string][]Websocket{} // {"own_phone_number": [...]}
	wsMapLock   sync.RWMutex               // 保护WS map的并发访问
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
	return Websocket{id, conn, sync.Mutex{}}
}

// 安全的WebSocket添加函数
func AddWebSocket(number string, ws Websocket) {
	wsMapLock.Lock()
	defer wsMapLock.Unlock()

	WS[number] = append(WS[number], ws)
	log.Infof("WebSocket连接已添加: %s, 当前连接数: %d", number, len(WS[number]))
}

// 安全的WebSocket移除函数
func RemoveWs(number, id string) {
	wsMapLock.Lock()
	defer wsMapLock.Unlock()

	for i, w := range WS[number] {
		if w.Id == id {
			WS[number] = append(WS[number][:i], WS[number][i+1:]...)
			log.Infof("WebSocket连接已移除: %s, 剩余连接数: %d", number, len(WS[number]))
			return
		}
	}
	log.Warnf("未找到要移除的WebSocket连接: %s, ID: %s", number, id)
}

// 安全的获取WebSocket连接副本
func getWebSocketCopy(number string) []Websocket {
	wsMapLock.RLock()
	defer wsMapLock.RUnlock()

	conns, exists := WS[number]
	if !exists {
		return nil
	}

	// 创建副本以避免在发送时持有锁
	result := make([]Websocket, len(conns))
	copy(result, conns)
	return result
}

func HeartBeatLoop() {
	for {
		time.Sleep(5 * time.Second)

		// 获取所有需要发送心跳的连接
		wsMapLock.RLock()
		allConnections := make(map[string][]Websocket)
		for number, conns := range WS {
			if len(conns) > 0 {
				allConnections[number] = make([]Websocket, len(conns))
				copy(allConnections[number], conns)
			}
		}
		wsMapLock.RUnlock()

		if len(allConnections) <= 0 {
			continue
		}

		for number, conns := range allConnections {
			log.Debugf("发送心跳到 %s, 连接数: %d", number, len(conns))
			for _, ws := range conns {
				// 使用连接级别的锁保证线程安全
				ws.Mu.Lock()
				if err := ws.Conn.WriteMessage(websocket.TextMessage, []byte(heart)); err != nil {
					log.Errorf("心跳发送失败 %s: %v", ws.Id, err)
					// 发送失败，移除连接
					go RemoveWs(number, ws.Id)
				}
				ws.Mu.Unlock()
			}
		}
	}
}

func WsSendSMS(number string, msg Msg) {
	m := WsMsg{msg}
	b, err := json.Marshal(&m)
	if err != nil {
		log.Errorf("JSON序列化失败: %v", err)
		return
	}

	// 获取连接副本
	conns := getWebSocketCopy(number)
	if len(conns) == 0 {
		log.Debugf("没有WebSocket连接用于号码: %s", number)
		return
	}

	log.Debugf("准备发送消息到 %s 的 %d 个WebSocket连接", number, len(conns))

	// 发送到所有连接
	for _, w := range conns {
		// 使用连接级别的锁保证线程安全
		w.Mu.Lock()
		err := w.Conn.WriteMessage(websocket.TextMessage, b)
		w.Mu.Unlock()

		if err != nil {
			log.Errorf("WebSocket写入失败 %s: %v", w.Id, err)
			// 写入失败，移除连接
			go RemoveWs(number, w.Id)
		} else {
			log.Debugf("消息成功发送到WebSocket: %s", w.Id)
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

// 清理无效连接
func CleanupInvalidConnections() {
	wsMapLock.Lock()
	defer wsMapLock.Unlock()

	for number, conns := range WS {
		validConns := make([]Websocket, 0)
		for _, ws := range conns {
			// 尝试发送ping来检查连接是否有效
			ws.Mu.Lock()
			err := ws.Conn.WriteMessage(websocket.PingMessage, nil)
			ws.Mu.Unlock()

			if err == nil {
				validConns = append(validConns, ws)
			} else {
				log.Infof("清理无效WebSocket连接: %s", ws.Id)
			}
		}
		WS[number] = validConns
	}
}
