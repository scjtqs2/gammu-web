package smsd

/*
#cgo pkg-config: gammu
#include <stdlib.h>
#include <gammu.h>
#include <string.h>

void sendCallback(GSM_StateMachine *sm, int status, int msgRef, void *data) {
    if (status==0) {
        *((GSM_Error *) data) = ERR_NONE;
    } else {
        *((GSM_Error *) data) = ERR_UNKNOWN;
    }
}
void setStatusCallback(GSM_StateMachine *sm, GSM_Error *status) {
    GSM_SetSendSMSStatusCallback(sm, sendCallback, status);
}
GSM_Debug_Info *debug_info;
void setDebug() {
    debug_info = GSM_GetGlobalDebug();
    GSM_SetDebugFileDescriptor(stderr, TRUE, debug_info);
    GSM_SetDebugLevel("textall", debug_info);
}
*/
import "C"
import (
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
	"unsafe"

	log "github.com/sirupsen/logrus"
)

var (
	lastSms           C.GSM_SMSMessage
	processedSMSCount = 0
)

// GSMError 包装 C.GSM_Error 的本地类型
type GSMError C.GSM_Error

// Error 实现 error 接口
func (e GSMError) Error() string {
	return C.GoString(C.GSM_ErrorString(C.GSM_Error(e)))
}

// ToGSMError 将 C.GSM_Error 转换为 GSMError
func ToGSMError(e C.GSM_Error) GSMError {
	return GSMError(e)
}

func NewError(descr string, g C.GSM_Error) error {
	return fmt.Errorf("[%s] %s", descr, ToGSMError(g).Error())
}

type EncodeError struct {
	g C.GSM_Error
}

func (e EncodeError) Error() string {
	return fmt.Sprintf(
		"[EncodeMultiPartSMS] %s", C.GoString(C.GSM_ErrorString(C.GSM_Error(e.g))),
	)
}

// StateMachine
type StateMachine struct {
	g       *C.GSM_StateMachine
	smsc    C.GSM_SMSC // SMS Center information
	number  string     // Own phone number
	country string     // Country code
	status  C.GSM_Error

	Timeout time.Duration // Default 15s
}

// Creates new state maschine using cf configuration file or default configuration file `~/.gammurc` if cf == "".
func NewStateMachine(cf string) (*StateMachine, error) {
	if os.Getenv("GAMMU_DEBUG") == "true" || os.Getenv("GAMMU_DEBUG") == "1" {
		C.setDebug()
	}
	var config *C.INI_Section
	if cf != "" {
		cs := C.CString(cf)
		defer C.free(unsafe.Pointer(cs))
		if e := C.GSM_FindGammuRC(&config, cs); e != C.ERR_NONE {
			return nil, NewError("FindGammuRC", e)
		}
	} else {
		if e := C.GSM_FindGammuRC(&config, nil); e != C.ERR_NONE {
			return nil, NewError("FindGammuRC", e)
		}
	}
	defer C.INI_Free(config)

	C.GSM_InitLocales((*C.char)(C.NULL))

	sm := new(StateMachine)
	sm.g = C.GSM_AllocStateMachine()
	if sm.g == nil {
		log.Fatal("GammuInit", "State Machine initial error: out of memory")
	}

	if e := C.GSM_ReadConfig(config, C.GSM_GetConfig(sm.g, 0), 0); e != C.ERR_NONE {
		sm.free()
		return nil, NewError("ReadConfig", e)
	}
	C.GSM_SetConfigNum(sm.g, 1)
	sm.Timeout = 15 * time.Second

	runtime.SetFinalizer(sm, (*StateMachine).free)
	return sm, nil
}

func (sm *StateMachine) free() {
	if sm.IsConnected() {
		sm.Disconnect()
	}
	C.GSM_FreeStateMachine(sm.g)
	sm.g = nil
}

func (sm *StateMachine) Connect() error {
	if e := C.GSM_InitConnection(sm.g, 1); e != C.ERR_NONE {
		sm.status = e
		return NewError("InitConnection", e)
	}
	C.setStatusCallback(sm.g, &sm.status)
	sm.smsc.Location = 1
	if e := C.GSM_GetSMSC(sm.g, &sm.smsc); e != C.ERR_NONE {
		sm.status = e
		return NewError("GetSMSC", e)
	}

	// 启用来电通知
	C.GSM_SetIncomingCall(sm.g, C.TRUE)

	// 注册通话回调
	registerCallCallback(sm.g)

	return nil
}

func (sm *StateMachine) IsConnected() bool {
	return C.GSM_IsConnected(sm.g) != 0
}

func (sm *StateMachine) Disconnect() error {
	if e := C.GSM_TerminateConnection(sm.g); e != C.ERR_NONE {
		sm.status = e
		return NewError("TerminateConnection", e)
	}
	return nil
}

func (sm *StateMachine) GetMemory(mem_type C.GSM_MemoryType, location int) (C.GSM_SubMemoryEntry, error) {
	mem_entry := C.GSM_MemoryEntry{MemoryType: mem_type, Location: (C.int)(location)}
	if e := C.GSM_GetMemory(sm.g, &mem_entry); e != C.ERR_NONE {
		return C.GSM_SubMemoryEntry{}, NewError("GetMemory", e)
	}
	return mem_entry.Entries[0], nil
}

func (sm *StateMachine) GetOwnNumber() string {
	if sm.number != "" {
		return sm.number
	}
	for {
		entry, e := sm.GetMemory(C.MEM_ON, 1)
		if e != nil {
			log.Errorf("GammuGetMem %v", e)
			log.Warn("GammuGetMem: 5秒后重新尝试获取SIM卡内存...")
			time.Sleep(5 * time.Second)
			continue
		}

		// 获取SIM卡中的原始号码
		rawNumber := encodeUTF8(&entry.Text[0])
		log.Infof("从SIM卡读取的原始号码: %s", rawNumber)

		// 清理号码格式（移除可能的多余前缀）
		cleanedNumber := cleanPhoneNumber(rawNumber)

		// 如果清理后的号码已经包含国家代码，就不重复添加
		if strings.HasPrefix(cleanedNumber, "+") {
			sm.number = cleanedNumber
		} else {
			// 只有号码不以+开头时才添加国家代码
			sm.number = sm.country + cleanedNumber
		}

		log.Infof("格式化后的本机号码: %s", sm.number)
		break
	}
	return sm.number
}

// 清理手机号码格式
func cleanPhoneNumber(number string) string {
	if number == "" {
		return number
	}

	// 移除所有非数字字符（除了+号）
	var result strings.Builder
	for _, ch := range number {
		if ch == '+' || (ch >= '0' && ch <= '9') {
			result.WriteRune(ch)
		}
	}

	cleaned := result.String()

	// 处理中国的特殊情况：86开头的号码可能已经包含国家代码
	if strings.HasPrefix(cleaned, "86") && len(cleaned) > 2 {
		// 如果以86开头且后面还有数字，可能是已经包含国家代码
		return "+" + cleaned
	}

	// 处理+86开头的号码（确保格式正确）
	if strings.HasPrefix(cleaned, "+86") {
		return cleaned
	}

	return cleaned
}
func (sm *StateMachine) GetCountryCode() string {
	if sm.country != "" {
		return sm.country
	}
	netinfo := C.GSM_NetworkInfo{}
	for {
		if e := C.GSM_GetNetworkInfo(sm.g, &netinfo); e != C.ERR_NONE {
			log.Errorf("GetCountryCode %v", e)
			log.Warnf("GammuGetCountryCode %s", "Tring to get country code of phone again after 5 seconds......")
			time.Sleep(5 * time.Second)
			continue
		}
		sm.country = parseCountry(encodeUTF8(&netinfo.NetworkName[0]))
		break
	}
	return sm.country
}

func (sm *StateMachine) Reset() error {
	if e := C.GSM_Reset(sm.g, 0); e != C.ERR_NONE {
		sm.status = e
		return NewError("Reset", e)
	}
	return nil
}

func (sm *StateMachine) HardReset() error {
	if e := C.GSM_Reset(sm.g, 1); e != C.ERR_NONE {
		sm.status = e
		return NewError("Reset", e)
	}
	return nil
}

func decodeUTF8(out *C.uchar, in string) {
	// 检查输出指针是否为空
	if out == nil {
		log.Error("decodeUTF8: 输出指针为空")
		return
	}

	// 检查输入字符串是否为空
	if in == "" {
		// 清空输出缓冲区
		C.memset(unsafe.Pointer(out), 0, 1)
		return
	}

	// 限制输入字符串长度
	maxInputLength := 1000
	if len(in) > maxInputLength {
		log.Warnf("decodeUTF8: 输入字符串长度 %d 超过限制，截断为 %d", len(in), maxInputLength)
		in = in[:maxInputLength]
	}

	cin := C.CString(in)
	defer C.free(unsafe.Pointer(cin))

	C.DecodeUTF8(out, cin, C.ulong(len(in)))
}

func encodeUnicode(out *C.uchar, in string) {
	// 检查输出指针是否为空
	if out == nil {
		log.Error("encodeUnicode: 输出指针为空")
		return
	}

	// 检查输入字符串是否为空
	if in == "" {
		// 清空输出缓冲区
		C.memset(unsafe.Pointer(out), 0, 1)
		return
	}

	// 限制输入字符串长度
	maxInputLength := 1000
	if len(in) > maxInputLength {
		log.Warnf("encodeUnicode: 输入字符串长度 %d 超过限制，截断为 %d", len(in), maxInputLength)
		in = in[:maxInputLength]
	}

	cin := C.CString(in)
	defer C.free(unsafe.Pointer(cin))

	C.EncodeUnicode(out, cin, C.ulong(len(in)))
}

func (sm *StateMachine) sendSMS(sms *C.GSM_SMSMessage, number string, report bool) error {
	C.CopyUnicodeString(&sms.SMSC.Number[0], &sm.smsc.Number[0])
	decodeUTF8(&sms.Number[0], number)
	if report {
		sms.PDU = C.SMS_Status_Report
	} else {
		sms.PDU = C.SMS_Submit
	}
	// Send mepssage
	sm.status = C.ERR_TIMEOUT
	if e := C.GSM_SendSMS(sm.g, sms); e != C.ERR_NONE {
		return NewError("SendSMS", e)
	}
	// Wait for reply
	t := time.Now()
	for time.Since(t) < sm.Timeout {
		C.GSM_ReadDevice(sm.g, C.TRUE)
		if sm.status == C.ERR_NONE {
			// Message sent OK
			break
		} else if sm.status != C.ERR_TIMEOUT {
			// Error
			break
		}
	}
	if sm.status != C.ERR_NONE {
		return NewError("ReadDevice", sm.status)
	}
	return nil
}

func (sm *StateMachine) SendSMS(number, text string, report bool) error {
	var sms C.GSM_SMSMessage
	decodeUTF8(&sms.Text[0], text)
	sms.UDH.Type = C.UDH_NoUDH
	sms.Coding = C.SMS_Coding_Default_No_Compression
	sms.Class = 1
	return sm.sendSMS(&sms, number, report)
}

func (sm *StateMachine) SendLongSMS(number, text string, report bool) error {
	// Fill in SMS info
	var smsInfo C.GSM_MultiPartSMSInfo
	C.GSM_ClearMultiPartSMSInfo(&smsInfo)
	smsInfo.Class = 1
	smsInfo.EntriesNum = 1
	smsInfo.UnicodeCoding = C.FALSE
	// Check for non-ASCII rune
	for _, r := range text {
		if r > 0x7F {
			smsInfo.UnicodeCoding = C.TRUE
			break
		}
	}
	smsInfo.Entries[0].ID = C.SMS_ConcatenatedTextLong
	msgUnicode := (*C.uchar)(C.calloc(C.size_t(len(text)+1), 2))
	defer C.free(unsafe.Pointer(msgUnicode))
	decodeUTF8(msgUnicode, text)
	smsInfo.Entries[0].Buffer = msgUnicode
	// Prepare multipart message
	var msms C.GSM_MultiSMSMessage
	if e := C.GSM_EncodeMultiPartSMS(nil, &smsInfo, &msms); e != C.ERR_NONE {
		return EncodeError{e}
	}
	// Send message
	for i := 0; i < int(msms.Number); i++ {
		if e := sm.sendSMS(&msms.SMS[i], number, report); e != nil {
			return e
		}
	}
	return nil
}

func encodeUTF8(in *C.uchar) string {
	if in == nil {
		log.Warn("encodeUTF8: 收到空指针输入")
		return ""
	}

	// 获取长度前检查指针有效性
	l := C.UnicodeLength(in)
	if l <= 0 || l > 10000 {
		return ""
	}

	// 使用更安全的方式分配内存
	outSize := int(l)*3 + 1 // UTF8可能最多3字节每个字符
	out := (*C.char)(C.malloc(C.size_t(outSize)))
	defer C.free(unsafe.Pointer(out))

	if out == nil {
		log.Error("encodeUTF8: 内存分配失败")
		return ""
	}

	// 确保以null结尾
	C.memset(unsafe.Pointer(out), 0, C.size_t(outSize))

	// 编码UTF8
	C.EncodeUTF8(out, in)

	return C.GoString(out)
}

// 增强的 goTime 函数，添加更多保护
func goTime(t *C.GSM_DateTime) time.Time {
	if t == nil {
		log.Warn("收到空的 GSM_DateTime 指针，使用当前时间")
		return time.Now()
	}

	// 检查关键字段是否为0，这可能表示无效的时间
	if t.Year == 0 && t.Month == 0 && t.Day == 0 {
		log.Warn("GSM_DateTime 所有字段都为0，使用当前时间")
		return time.Now()
	}

	year := int(t.Year)
	month := int(t.Month)
	day := int(t.Day)
	hour := int(t.Hour)
	minute := int(t.Minute)
	second := int(t.Second)

	// 更严格的时间验证
	currentYear := time.Now().Year()
	if year < 2000 || year > currentYear+1 {
		log.Warnf("年份 %d 不合理，使用当前时间", year)
		return time.Now()
	}
	if month < 1 || month > 12 {
		log.Warnf("月份 %d 不合理，使用当前时间", month)
		return time.Now()
	}

	// 检查每月的天数
	maxDays := 31
	if month == 2 {
		if (year%4 == 0 && year%100 != 0) || (year%400 == 0) {
			maxDays = 29 // 闰年
		} else {
			maxDays = 28
		}
	} else if month == 4 || month == 6 || month == 9 || month == 11 {
		maxDays = 30
	}

	if day < 1 || day > maxDays {
		log.Warnf("日期 %d 不合理，使用当前时间", day)
		return time.Now()
	}
	if hour < 0 || hour > 23 {
		log.Warnf("小时 %d 不合理，使用当前时间", hour)
		return time.Now()
	}
	if minute < 0 || minute > 59 {
		log.Warnf("分钟 %d 不合理，使用当前时间", minute)
		return time.Now()
	}
	if second < 0 || second > 59 {
		log.Warnf("秒 %d 不合理，使用当前时间", second)
		return time.Now()
	}

	return time.Date(
		year, time.Month(month), day,
		hour, minute, second, 0,
		time.UTC,
	).Add(-time.Second * time.Duration(t.Timezone)).Local()
}

type SMS struct {
	Time     time.Time
	SMSCTime time.Time
	Number   string
	Report   bool // True if this message is a delivery report
	Body     string
}

// Read and deletes first available message.
// Returns io.EOF if there are no more messages to read
func (sm *StateMachine) GetSMS() (sms SMS, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("GetSMS发生panic: %v", r)
			log.Errorf("堆栈信息: %s", string(debug.Stack()))
			err = fmt.Errorf("GetSMS panic: %v", r)
		}
	}()

	var msms C.GSM_MultiSMSMessage

	// 使用 GetNextSMS，从头开始读取
	if e := C.GSM_GetNextSMS(sm.g, &msms, C.TRUE); e != C.ERR_NONE {
		if e == C.ERR_EMPTY {
			return sms, io.EOF
		}
		return sms, NewError("GetNextSMS", e)
	}

	// 检查消息数量是否合理
	if msms.Number <= 0 || msms.Number > 10 {
		log.Warnf("异常的消息数量: %d，跳过处理", msms.Number)
		return sms, fmt.Errorf("异常的消息数量: %d", msms.Number)
	}

	// 取第一个条目的发件人/时间作为该 "消息" 的元信息
	first := &msms.SMS[0]

	// 安全检查：确保第一个短信段有效
	if !isValidSMSMessage(first) {
		log.Warn("第一个短信段无效，跳过处理")
		return sms, fmt.Errorf("无效的短信数据")
	}

	// 安全地获取号码
	sms.Number = encodeUTF8(&first.Number[0])

	// 安全地处理时间
	sms.Time = goTime(&first.DateTime)
	sms.SMSCTime = goTime(&first.SMSCTime)

	// 将多段拼接
	for i := 0; i < int(msms.Number); i++ {
		s := &msms.SMS[i]

		// 安全检查：确保消息结构有效
		if !isValidSMSMessage(s) {
			log.Warnf("跳过无效的短信段 %d", i)
			continue
		}

		if s.Coding == C.SMS_Coding_8bit {
			// 跳过二进制消息体
			continue
		}

		// 安全地获取文本内容
		text := encodeUTF8(&s.Text[0])
		sms.Body += text

		if s.PDU == C.SMS_Status_Report {
			sms.Report = true
		}
	}

	// 安全的删除操作
	if err := sm.safeDeleteSMS(&msms); err != nil {
		log.Errorf("删除短信失败: %v", err)
		return sms, err
	}

	return sms, nil
}

// 增强的安全检查函数 - 现在接收指针参数
func isValidSMSMessage(s *C.GSM_SMSMessage) bool {
	if s == nil {
		log.Warn("短信消息结构为空指针")
		return false
	}

	// 放宽位置值检查范围，某些设备可能有更大的位置值
	if s.Location < 0 || s.Location > 1000000 { // 从10000增加到1000000
		log.Warnf("异常的位置值: %d", s.Location)
		// 不立即返回false，继续检查其他字段
	}

	// 检查时间字段的合理性
	// 检查年份是否在合理范围内
	if s.DateTime.Year < 0 || s.DateTime.Year > 2900 {
		log.Warnf("异常的年份值: %d", s.DateTime.Year)
		return false
	}

	// 检查号码字段
	number := encodeUTF8(&s.Number[0])
	if number == "" {
		log.Warn("号码字段为空")
		return false
	}

	// 如果位置值异常但其他字段正常，仍然认为是有效的
	return true
}

// 增强：安全的删除函数
func (sm *StateMachine) safeDeleteSMS(msms *C.GSM_MultiSMSMessage) error {
	if msms == nil {
		return fmt.Errorf("消息结构为空")
	}

	for i := 0; i < int(msms.Number); i++ {
		s := &msms.SMS[i]

		// 额外的安全检查
		if !isValidSMSMessage(s) {
			log.Warnf("跳过删除无效的短信段 %d", i)
			continue
		}

		// 设置 Flat folder
		s.Folder = 0

		// 使用互斥锁保护CGO调用
		if e := C.GSM_DeleteSMS(sm.g, s); e != C.ERR_NONE {
			// 不是致命错误，记录但继续
			log.Warnf("删除短信段 %d 失败: %v", i, ToGSMError(e))
			// 继续删除其他段，不立即返回错误
		} else {
			log.Debugf("成功删除短信段 %d", i)
		}
	}

	return nil
}

// 重置短信状态 - 使用现有的函数
func (sm *StateMachine) ResetSMSStatus() error {
	// 方法1: 使用通用的重置函数
	if e := C.GSM_Reset(sm.g, 0); e != C.ERR_NONE {
		return NewError("Reset", e)
	}

	// 方法2: 重新初始化连接
	if err := sm.Reconnect(); err != nil {
		return err
	}

	return nil
}

// 重新连接
func (sm *StateMachine) Reconnect() error {
	if sm.IsConnected() {
		if err := sm.Disconnect(); err != nil {
			log.Warnf("断开连接失败: %v", err)
		}
	}

	time.Sleep(2 * time.Second)

	if err := sm.Connect(); err != nil {
		return fmt.Errorf("重新连接失败: %v", err)
	}

	return nil
}
