package smsd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// ForwardConn 结构体中
type ForwardConn struct {
	ForwardEnabled     bool   `json:"forward_enabled"`
	ForwardCallEnabled bool   `json:"forward_call_enabled"`
	ForwardURL         string `json:"forward_url"`
	ForwardSecret      string `json:"forward_secret"`
	ForwardTimeout     int    `json:"forward_timeout"`
	ForwardChan        chan []Msg
	CallForwardURL     string          `json:"call_forward_url"` // 新增：通话记录转发URL
	CallChan           chan CallRecord // 新增：通话记录通道
	workerCount        int
	callWorkerCount    int // 新增：通话记录worker数量
	wg                 sync.WaitGroup
	ctx                context.Context
	cancel             context.CancelFunc
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

// initForward 初始化
func initForward() *ForwardConn {
	forwardEnabled := os.Getenv("FORWARD_ENABLED") == "true" || os.Getenv("FORWARD_ENABLED") == "1"
	forwardURL := getEnv("FORWARD_URL", "http://forwardsms:8080/api/v1/sms/receive")
	forwardSecret := os.Getenv("FORWARD_SECRET")
	forwardTimeout := getEnvInt("FORWARD_TIMEOUT", 30)
	forwardCallEnabled := os.Getenv("CALL_FORWARD_ENABLED") == "true" || os.Getenv("CALL_FORWARD_ENABLED") == "1"
	// 新增：通话记录转发URL配置
	callForwardURL := getEnv("CALL_FORWARD_URL", "http://forwardsms:8080/api/v1/call/receive")

	// 验证必要的配置
	if forwardEnabled && forwardSecret == "" {
		log.Warn("转发已启用但未设置 FORWARD_SECRET，转发功能可能无法正常工作")
	}

	ctx, cancel := context.WithCancel(context.Background())
	conn := &ForwardConn{
		ForwardEnabled:     forwardEnabled,
		ForwardCallEnabled: forwardCallEnabled,
		ForwardURL:         forwardURL,
		ForwardSecret:      forwardSecret,
		ForwardTimeout:     forwardTimeout,
		ForwardChan:        make(chan []Msg, 100),
		CallForwardURL:     callForwardURL,            // 新增
		CallChan:           make(chan CallRecord, 50), // 新增：通话记录通道
		workerCount:        1,
		callWorkerCount:    1, // 新增：通话记录worker数量
		ctx:                ctx,
		cancel:             cancel,
	}

	// 启动短信worker
	for i := 0; i < conn.workerCount; i++ {
		conn.wg.Add(1)
		go conn.processSMSWorker(i)
	}

	// 新增：启动通话记录worker
	for i := 0; i < conn.callWorkerCount; i++ {
		conn.wg.Add(1)
		go conn.processCallWorker(i)
	}

	log.Infof("短信转发服务初始化完成: 启用=%v, 短信Worker数量=%d, 通话Worker数量=%d",
		forwardEnabled, conn.workerCount, conn.callWorkerCount)
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
	log.Info("开始关闭转发服务...")
	f.cancel()

	// 等待所有 worker 完成
	done := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(done)
	}()

	// 带超时的等待
	select {
	case <-done:
		log.Info("转发服务已优雅关闭")
	case <-time.After(10 * time.Second):
		log.Warn("转发服务关闭超时，强制关闭")
	}

	close(f.ForwardChan)
	close(f.CallChan)
}

// processSMSWorker 处理转发的worker
func (f *ForwardConn) processSMSWorker(workerID int) {
	defer f.wg.Done()

	log.Infof("转发worker %d 启动", workerID) // 添加启动日志

	for {
		select {
		case msgs, ok := <-f.ForwardChan:
			if !ok {
				log.Infof("转发worker %d 退出", workerID) // 修复：workerID 是 int 类型
				return
			}
			f.processBatchSMS(msgs, workerID)
		case <-f.ctx.Done():
			log.Infof("转发worker %d 收到退出信号", workerID) // 修复：workerID 是 int 类型
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
			log.Infof("重试成功: 短信ID %s (第%d次重试)", msg.ID, i+1)
			return true
		} else {
			log.Warnf("重试失败: 短信ID %s (第%d次重试): %v", msg.ID, i+1, err)
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

	// 创建自定义解析器避免DNS问题
	dialer := &net.Dialer{
		Timeout:   time.Duration(f.ForwardTimeout) * time.Second / 2,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		DialContext:         dialer.DialContext,
		MaxIdleConns:        10,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableCompression:  true,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(f.ForwardTimeout) * time.Second,
	}

	reqData := SMSRequest{
		Secret:    f.ForwardSecret,
		Number:    record.Number,
		Time:      record.Time.Format("2006-01-02 15:04:05"),
		Text:      record.Text,
		Source:    "gammu-web",
		PhoneID:   f.getPhoneId(record),
		SMSID:     record.ID,
		Timestamp: strconv.FormatInt(record.Time.Unix(), 10),
	}

	jsonData, err := json.Marshal(reqData)
	if err != nil {
		return fmt.Errorf("序列化JSON失败: %v", err)
	}

	log.Debugf("尝试转发短信: %s 到: %s", record.Text, f.ForwardURL)

	req, err := http.NewRequest("POST", f.ForwardURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("创建HTTP请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Gammu-SMSD-Go/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送HTTP请求失败: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败: %v", err)
	}

	if resp.StatusCode == http.StatusOK {
		log.Infof("✓ 短信ID: %s 成功转发 (HTTP %d)", record.ID, resp.StatusCode)
		if len(body) > 0 {
			log.Debugf("服务响应: %s", string(body))
		}
		return nil
	} else {
		return fmt.Errorf("短信ID: %s 转发失败 - HTTP 状态码: %d, 响应: %s",
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

// CallRequest 发送到转发服务的通话记录请求结构
type CallRequest struct {
	Secret    string `json:"secret"`
	Number    string `json:"number"`
	Name      string `json:"name"`
	Time      string `json:"time"`
	Type      string `json:"type"`
	Duration  int    `json:"duration"`
	Source    string `json:"source"`
	PhoneID   string `json:"phone_id"`
	Timestamp string `json:"timestamp"`
}

// 新增：处理通话记录的worker
func (f *ForwardConn) processCallWorker(workerID int) {
	defer f.wg.Done()

	log.Infof("通话记录转发worker %d 启动", workerID)

	for {
		select {
		case call, ok := <-f.CallChan:
			if !ok {
				log.Infof("通话记录转发worker %d 退出", workerID)
				return
			}
			f.forwardCall(call, workerID)
		case <-f.ctx.Done():
			log.Infof("通话记录转发worker %d 收到退出信号", workerID)
			return
		}
	}
}

func (f *ForwardConn) checkCallEnable() bool {
	return f.ForwardCallEnabled
}

// forwardCall 转发通话记录
func (f *ForwardConn) forwardCall(call CallRecord, workerID int) {
	if !f.checkCallEnable() {
		return
	}

	reqData := CallRequest{
		Secret:    f.ForwardSecret,
		Number:    call.Number,
		Name:      call.Name,
		Time:      call.Time.Format("2006-01-02 15:04:05"),
		Type:      call.Type,
		Duration:  call.Duration,
		Source:    "gammu-web",
		PhoneID:   f.getPhoneId(Msg{}), // 使用空的Msg，因为通话记录没有Msg结构
		Timestamp: strconv.FormatInt(call.Time.Unix(), 10),
	}

	// 序列化为 JSON
	jsonData, err := json.Marshal(reqData)
	if err != nil {
		log.Errorf("worker %d 通话记录序列化失败: %v", workerID, err)
		return
	}

	log.Debugf("worker %d 尝试转发通话记录: %s %s", workerID, call.Number, call.Type)

	// 创建带超时的HTTP客户端
	client := &http.Client{
		Timeout: time.Duration(f.ForwardTimeout) * time.Second,
	}

	// 创建请求
	req, err := http.NewRequest("POST", f.CallForwardURL, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Errorf("worker %d 创建通话记录HTTP请求失败: %v", workerID, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Gammu-SMSD-Go/1.0")

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		log.Errorf("worker %d 发送通话记录HTTP请求失败: %v", workerID, err)
		return
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("worker %d 读取通话记录响应失败: %v", workerID, err)
		return
	}

	if resp.StatusCode == http.StatusOK {
		log.Infof("✓ worker %d 通话记录成功转发: %s %s (HTTP %d)",
			workerID, call.Number, call.Type, resp.StatusCode)
		if len(body) > 0 {
			log.Debugf("服务响应: %s", string(body))
		}
	} else {
		log.Errorf("worker %d 通话记录转发失败 - 号码: %s, 类型: %s, HTTP状态码: %d, 响应: %s",
			workerID, call.Number, call.Type, resp.StatusCode, string(body))
	}
}

// PutCall 安全地放入通话记录
func (f *ForwardConn) PutCall(call CallRecord) {
	if !f.checkEnable() {
		return
	}

	select {
	case f.CallChan <- call:
		// 成功放入队列
	case <-f.ctx.Done():
		// 服务已关闭
		log.Warn("转发服务已关闭，丢弃通话记录")
	default:
		// 队列已满，使用goroutine避免阻塞
		go func(call CallRecord) {
			select {
			case f.CallChan <- call:
			case <-time.After(5 * time.Second):
				log.Warn("通话记录转发队列已满，丢弃记录")
			}
		}(call)
	}
}
