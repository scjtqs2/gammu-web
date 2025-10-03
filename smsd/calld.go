package smsd

/*
#cgo pkg-config: gammu
#include <stdlib.h>
#include <gammu.h>


// 前向声明 Go 导出的函数（告诉 C 有这么个函数）
extern void goCallHandler(GSM_StateMachine *s, GSM_Call *call, void *user_data);

// C 层桥接函数
static void callHandlerBridge(GSM_StateMachine *s, GSM_Call *call, void *user_data) {
    goCallHandler(s, call, user_data);
}

// 注册回调
static void registerCallCallback(GSM_StateMachine *s) {
    GSM_SetIncomingCallCallback(s, callHandlerBridge, NULL);
}
*/
import "C"
import (
	"time"
	"unsafe"

	log "github.com/sirupsen/logrus"
)

// CallRecord 通话记录结构
type CallRecord struct {
	Time     time.Time
	Number   string
	Name     string
	Type     string // "incoming", "outgoing", "missed", "ended", "unknown"
	Duration int    // 通话时长（秒）
}

// callEvents 通道：用于把 C 层/其它地方产生的通话事件传到 Go 层。
// 注意：要让 libgammu 把事件写到这个通道，需要在 C 层注册回调并在回调内部调用一个导出函数把数据送到 Go。
// 这里提供 channel，便于后续连接 C 回调或其它来源。
var callEvents = make(chan CallRecord, 200)

// PushCallEvent 可以从 C 回调或其它代码中调用，把通话事件放入队列（非阻塞）。
func PushCallEvent(ev CallRecord) {
	select {
	case callEvents <- ev:
	default:
		// 队列已满，丢弃并记录
		log.Warnf("callEvents 队列已满，丢弃来电记录: %v", ev)
	}
}

//export goCallHandler
func goCallHandler(s *C.GSM_StateMachine, call *C.GSM_Call, user_data unsafe.Pointer) {
	defer func() {
		if r := recover(); r != nil {
			// log.Errorf("goCallHandler发生panic: %v", r)
		}
	}()

	// 安全检查
	if call == nil {
		// log.Warn("goCallHandler: 收到空的call指针")
		return
	}

	// log.Infof("goCallHandler triggered! Call status raw value: %d", call.Status)

	// 安全的号码提取
	var number string
	if &call.PhoneNumber[0] != nil {
		number = encodeUTF8(&call.PhoneNumber[0])
	} else {
		number = "未知号码"
	}

	status := call.Status

	// 过滤异常状态值
	if status > 1000 {
		// log.Warnf("异常的通话状态值: %d，号码: %s", status, number)
		return
	}

	var callType string
	switch status {
	case C.GSM_CALL_IncomingCall:
		callType = "incoming"
	case C.GSM_CALL_OutgoingCall:
		callType = "outgoing"
	case C.GSM_CALL_CallRemoteEnd, C.GSM_CALL_CallLocalEnd, C.GSM_CALL_CallEnd:
		callType = "ended"
	default:
		// log.Warnf("未处理的通话状态: %d 号码: %s", status, number)
		callType = "unknown"
	}

	rec := CallRecord{
		Time:     time.Now(),
		Number:   number,
		Name:     "",
		Type:     callType,
		Duration: 0,
	}

	PushCallEvent(rec)
}

// registerCallCallback 注册callback
func registerCallCallback(g *C.GSM_StateMachine) {
	C.registerCallCallback(g)
}
