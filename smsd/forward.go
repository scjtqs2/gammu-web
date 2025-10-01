package smsd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

type ForwardConn struct {
	ForwardEnabled bool   `json:"forward_enabled"`
	ForwardURL     string `json:"forward_url"`
	ForwardSecret  string `json:"forward_secret"`
	ForwardTimeout int    `json:"forward_timeout"`
	ForwardChan    chan []Msg
	workerCount    int
	wg             sync.WaitGroup
	ctx            context.Context
	cancel         context.CancelFunc
}

// SMSRequest 发送到转发服务的请求结构
type SMSRequest struct {
	Secret    string `json:"secret"`
	Number    string `json:"number"`
	Time      string `json:"time"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	PhoneID   string `json:"phone_id"`
	SMSID     string `json:"sms_id"`
	Timestamp string `json:"timestamp"`
}

// initForward 初始化转发服务
func initForward() *ForwardConn {
	forwardEnabled := os.Getenv("FORWARD_ENABLED") == "true" || os.Getenv("FORWARD_ENABLED") == "1"
	forwardURL := getEnv("FORWARD_URL", "http://forwardsms:8080/api/v1/sms/receive")
	forwardSecret := os.Getenv("FORWARD_SECRET")
	forwardTimeout := getEnvInt("FORWARD_TIMEOUT", 30)
	ctx, cancel := context.WithCancel(context.Background())
	conn := &ForwardConn{
		ForwardEnabled: forwardEnabled,
		ForwardURL:     forwardURL,
		ForwardSecret:  forwardSecret,
		ForwardTimeout: forwardTimeout,
		ForwardChan:    make(chan []Msg, 100),
		workerCount:    3, // 3个worker处理转发
		ctx:            ctx,
		cancel:         cancel,
	}
	// 启动多个worker
	for i := 0; i < conn.workerCount; i++ {
		conn.wg.Add(1)
		go conn.processSMSWorker(i)
	}
	return conn
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func (f *ForwardConn) checkEnable() bool {
	return f.ForwardEnabled
}

// Close 关闭转发服务
func (f *ForwardConn) Close() {
	f.cancel()
	f.wg.Wait()
	close(f.ForwardChan)
}

// processSMSWorker 处理转发的worker
func (f *ForwardConn) processSMSWorker(workerID int) {
	defer f.wg.Done()

	for {
		select {
		case msgs, ok := <-f.ForwardChan:
			if !ok {
				log.Infof("转发worker %d 退出", workerID)
				return
			}
			f.processBatchSMS(msgs, workerID)
		case <-f.ctx.Done():
			log.Infof("转发worker %s 收到退出信号", workerID)
			return
		}
	}
}

// processBatchSMS 批量处理短信
func (f *ForwardConn) processBatchSMS(msgs []Msg, workerID int) {
	if !f.checkEnable() {
		return
	}

	log.Infof("worker %d 开始处理 %d 条短信", workerID, len(msgs))

	successCount := 0
	failCount := 0

	for _, msg := range msgs {
		if err := f.forwardSMS(msg); err != nil {
			log.Errorf("worker %d 短信转发失败: %v", workerID, err)
			failCount++

			// 失败重试逻辑
			if f.retryForward(msg, 3) {
				successCount++
			} else {
				failCount++
			}
		} else {
			successCount++
		}
	}

	log.Infof("worker %d 处理完成: 成功 %d, 失败 %d", workerID, successCount, failCount)
}

// retryForward 重试转发
func (f *ForwardConn) retryForward(msg Msg, maxRetries int) bool {
	for i := 0; i < maxRetries; i++ {
		time.Sleep(time.Duration(i+1) * time.Second) // 指数退避

		if err := f.forwardSMS(msg); err == nil {
			log.Infof("重试成功: 短信ID %d (第%d次重试)", msg.ID, i+1)
			return true
		} else {
			log.Warnf("重试失败: 短信ID %d (第%d次重试): %v", msg.ID, i+1, err)
		}
	}
	return false
}

func (f *ForwardConn) getPhoneId(record Msg) string {
	if os.Getenv("PHONE_ID") != "" {
		return os.Getenv("PHONE_ID")
	}
	return fmt.Sprintf("SMS_%s", record.SelfNumber)
}

// forwardSMS 转发单条短信
func (f *ForwardConn) forwardSMS(record Msg) error {
	if !f.checkEnable() {
		return nil
	}

	reqData := SMSRequest{
		Secret:    f.ForwardSecret,
		Number:    record.Number,
		Time:      record.Time.Format("2006-01-02 15:04:05"), // 使用短信本身的时间
		Text:      record.Text,
		Source:    "gammu-web",
		PhoneID:   f.getPhoneId(record),
		SMSID:     record.ID,
		Timestamp: strconv.FormatInt(record.Time.Unix(), 10),
	}

	// 序列化为 JSON
	jsonData, err := json.Marshal(reqData)
	if err != nil {
		return fmt.Errorf("序列化JSON失败: %v", err)
	}

	log.Debugf("尝试转发短信: %s 到: %s", record.Text, f.ForwardURL)

	// 创建带超时的HTTP客户端
	client := &http.Client{
		Timeout: time.Duration(f.ForwardTimeout) * time.Second,
	}

	// 创建请求
	req, err := http.NewRequest("POST", f.ForwardURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("创建HTTP请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Gammu-SMSD-Go/1.0")

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送HTTP请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败: %v", err)
	}

	if resp.StatusCode == http.StatusOK {
		log.Infof("✓ 短信ID: %d 成功转发 (HTTP %d)", record.ID, resp.StatusCode)
		if len(body) > 0 {
			log.Debugf("服务响应: %s", string(body))
		}
		return nil
	} else {
		return fmt.Errorf("短信ID: %d 转发失败 - HTTP 状态码: %d, 响应: %s",
			record.ID, resp.StatusCode, string(body))
	}
}

// PutMsgs 安全地放入消息（批量）
func (f *ForwardConn) PutMsgs(msgs []Msg) {
	if !f.checkEnable() || len(msgs) == 0 {
		return
	}

	// 深度复制数据，避免数据竞争
	msgsCopy := make([]Msg, len(msgs))
	copy(msgsCopy, msgs)

	select {
	case f.ForwardChan <- msgsCopy:
		// 成功放入队列
	case <-f.ctx.Done():
		// 服务已关闭
		log.Warn("转发服务已关闭，丢弃短信")
	default:
		// 队列已满，使用goroutine避免阻塞
		go func(msgs []Msg) {
			select {
			case f.ForwardChan <- msgs:
			case <-time.After(5 * time.Second):
				log.Warn("转发队列已满，丢弃短信")
			}
		}(msgsCopy)
	}
}
