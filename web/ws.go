package web

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"

	"github.com/ctaoist/gammu-web/config"
	"github.com/ctaoist/gammu-web/message"
	"github.com/ctaoist/gammu-web/smsd"
	log "github.com/sirupsen/logrus"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// cors
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
} // use default options

func wsHandler(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Errorf("WebSocket升级失败: %v", err)
		return
	}

	number := smsd.GetOwnNumber()
	id := message.GenerateId()

	// 创建新的WebSocket连接
	wsConn := message.NewWebSocket(id, ws)

	// 使用线程安全的方式添加连接
	message.AddWebSocket(number, wsConn)

	log.Infof("WebSocket连接已建立: %s (ID: %s)", number, id)

	defer func() {
		// 使用线程安全的方式移除连接
		message.RemoveWs(number, id)
		ws.Close()
		log.Infof("WebSocket连接已关闭: %s (ID: %s)", number, id)
	}()

	// 设置读取超时和其他参数
	ws.SetReadLimit(512) // 限制消息大小
	// ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	// ws.SetPongHandler(func(string) error {
	// 	ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	// 	return nil
	// })

	for {
		messageType, msg, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Errorf("WebSocket读取错误: %v", err)
			} else {
				log.Debugf("WebSocket连接正常关闭: %v", err)
			}
			break
		}

		// 处理不同类型的消息
		switch messageType {
		case websocket.TextMessage:
			log.Infof("WebSocket收到文本消息: %s", string(msg))
			// 可以在这里处理客户端发送的指令
			// 例如：{"type": "ping", "data": "..."}

			// 简单的ping-pong响应
			if string(msg) == "ping" {
				wsConn.Mu.Lock()
				err = ws.WriteMessage(websocket.TextMessage, []byte("pong"))
				wsConn.Mu.Unlock()
				if err != nil {
					log.Errorf("WebSocket响应ping失败: %v", err)
					break
				}
			}

		case websocket.BinaryMessage:
			log.Infof("WebSocket收到二进制消息，长度: %d", len(msg))

		case websocket.CloseMessage:
			log.Debug("WebSocket收到关闭消息")
			return

		case websocket.PingMessage:
			log.Debug("WebSocket收到Ping消息")
			wsConn.Mu.Lock()
			err = ws.WriteMessage(websocket.PongMessage, nil)
			wsConn.Mu.Unlock()
			if err != nil {
				log.Errorf("WebSocket响应Ping失败: %v", err)
				break
			}

		case websocket.PongMessage:
			log.Debug("WebSocket收到Pong消息")

		default:
			log.Warnf("WebSocket收到未知消息类型: %d", messageType)
		}
	}
}

func logHandler(w http.ResponseWriter, r *http.Request) {
	if config.LogFile == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{"retCode": -1, "errorMsg": "program has no log file"})
		return
	}
	fstate, err := os.Stat(config.LogFile)
	if err == nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		b := make([]byte, 500*1024)
		if fstate.Size()/1024 <= 500 { // 小于 500 kb
			b, err = ioutil.ReadFile(config.LogFile)
			if err != nil {
				json.NewEncoder(w).Encode(map[string]interface{}{"retCode": -1, "errorMsg": "can't read log file: " + err.Error()})
				return
			}
		} else { // 文件太大
			f, err := os.OpenFile(config.LogFile, os.O_RDONLY, os.ModePerm)
			defer f.Close()
			if err != nil {
				json.NewEncoder(w).Encode(map[string]interface{}{"retCode": -1, "errorMsg": "can't open log file: " + err.Error()})
				return
			}
			f.Seek(-500*1024, os.SEEK_END)
			f.Read(b)
		}
		w.Write(b)
	} else {
		json.NewEncoder(w).Encode(map[string]interface{}{"retCode": -1, "errorMsg": "log file not found"})
	}
}

func clearLog(w http.ResponseWriter, r *http.Request) {
	if err := os.Truncate(config.LogFile, 0); err != nil {
		log.Errorf("ClearLog Failed to truncate: %v", err)
		json.NewEncoder(w).Encode(map[string]interface{}{"retCode": -1, "errorMsg": "can't clear log file: " + err.Error()})
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"retCode": 0, "errorMsg": ""})
}
