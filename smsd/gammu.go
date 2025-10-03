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
	// 检查输入指针是否为空
	if in == nil {
		log.Warn("encodeUTF8: 收到空指针输入")
		return ""
	}

	// 获取 Unicode 字符串长度
	l := C.UnicodeLength(in)

	// 检查长度是否合理
	if l <= 0 {
		return ""
	}

	// 设置合理的最大长度限制（避免内存过度分配）
	const maxReasonableLength = 10000 // 短信通常不会超过这个长度
	if l > maxReasonableLength {
		log.Warnf("encodeUTF8: 字符串长度 %d 超过合理范围，限制为 %d", l, maxReasonableLength)
		l = maxReasonableLength
	}

	// 分配输出缓冲区（增加额外空间确保安全）
	outSize := l*2 + 1 // 额外的一个字符用于确保以null结尾
	out := make([]C.char, outSize)

	// 确保缓冲区以null结尾
	out[outSize-1] = 0

	// 编码UTF8
	C.EncodeUTF8(&out[0], in)

	// 转换为Go字符串前检查第一个字符是否为null
	if out[0] == 0 {
		log.Warn("encodeUTF8: 编码结果为空字符串")
		return ""
	}

	return C.GoString(&out[0])
}

func goTime(t *C.GSM_DateTime) time.Time {
	// 检查 C 结构体指针是否有效
	if t == nil {
		log.Warn("收到空的 GSM_DateTime 指针，使用当前时间")
		return time.Now()
	}

	// 检查日期字段的合理性
	year := int(t.Year)
	month := int(t.Month)
	day := int(t.Day)
	hour := int(t.Hour)
	minute := int(t.Minute)
	second := int(t.Second)

	// 基本验证
	if year < 2000 || year > 2100 {
		log.Warnf("年份 %d 不合理，使用当前时间", year)
		return time.Now()
	}
	if month < 1 || month > 12 {
		log.Warnf("月份 %d 不合理，使用当前时间", month)
		return time.Now()
	}
	if day < 1 || day > 31 {
		log.Warnf("日期 %d 不合理，使用当前时间", day)
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

	// 使用 GetNextSMS，从头开始读取（start = TRUE 表示从头开始扫描）
	// 如果你希望保持未删除，可以把最后一个参数设为 C.FALSE 然后手动删除。
	if e := C.GSM_GetNextSMS(sm.g, &msms, C.TRUE); e != C.ERR_NONE {
		if e == C.ERR_EMPTY {
			return sms, io.EOF
		}
		return sms, NewError("GetNextSMS", e)
	}

	// msms.Number 表示 msms.SMS[] 中的条目数（分段sms会放在一起）
	// 取第一个条目的发件人/时间作为该 "消息" 的元信息
	first := msms.SMS[0]
	sms.Number = encodeUTF8(&first.Number[0])
	sms.Time = goTime(&first.DateTime)
	sms.SMSCTime = goTime(&first.SMSCTime)

	// 将多段拼接
	for i := 0; i < int(msms.Number); i++ {
		s := msms.SMS[i]
		// 保存 lastSms 按需（若你确实需要全局 lastSms，可在这里赋值）
		lastSms = s

		if s.Coding == C.SMS_Coding_8bit {
			// 跳过二进制消息体（或按需处理）
			continue
		}
		sms.Body += encodeUTF8(&s.Text[0])
		if s.PDU == C.SMS_Status_Report {
			sms.Report = true
		}
	}

	// 如果你传入 C.TRUE 给 GSM_GetNextSMS，libgammu 在读取时就能做删除。
	// 但不同版本/驱动表现可能不一，若你想显式删除可以对 msms.SMS[0] 调用 GSM_DeleteSMS。
	// 我们这里尝试显式删除以确保：
	for i := 0; i < int(msms.Number); i++ {
		s := msms.SMS[i]
		// 设置 Flat folder（与之前逻辑一致）
		s.Folder = 0
		if e := C.GSM_DeleteSMS(sm.g, &s); e != C.ERR_NONE {
			// 不是致命错误，但返回包装后上层处理
			return sms, NewError("DeleteSMS", e)
		}
	}

	return sms, nil
}

func c_gsm_deleteSMS(sm *StateMachine, s *C.GSM_SMSMessage) {
	if s == nil {
		return
	}
	s.Folder = 0 // Flat
	if e := C.GSM_DeleteSMS(sm.g, s); e != C.ERR_NONE {
		log.Error("c_gsm_DeleteSMS", NewError("DeleteSMS", e))
	}
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
