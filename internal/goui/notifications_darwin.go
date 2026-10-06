package goui

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var notificationFramework struct {
	sync.Once
	ready bool
}

var notificationDelegate struct {
	sync.Once
	instance objc.ID
	err      error
}

type notificationBlockLayout struct {
	isa      uintptr
	flags    int32
	reserved int32
	invoke   uintptr
}

func installNotificationDelegate(center objc.ID, p *macNativePlatform) bool {
	notificationDelegate.Do(func() {
		protocol := objc.GetProtocol("UNUserNotificationCenterDelegate")
		if protocol == nil {
			notificationDelegate.err = fmt.Errorf("UNUserNotificationCenterDelegate unavailable")
			return
		}
		class, err := objc.RegisterClass("WaterNotificationDelegate", objc.GetClass("NSObject"), []*objc.Protocol{protocol}, nil, []objc.MethodDef{{
			Cmd: objc.RegisterName("userNotificationCenter:willPresentNotification:withCompletionHandler:"),
			Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID, _ objc.ID, completion objc.ID) {
				if completion == 0 {
					return
				}
				blockPointer := *(*unsafe.Pointer)(unsafe.Pointer(&completion))
				block := (*notificationBlockLayout)(blockPointer)
				const presentationOptions = uintptr((1 << 1) | (1 << 2) | (1 << 3) | (1 << 4))
				purego.SyscallN(block.invoke, uintptr(completion), presentationOptions)
			},
		}})
		if err != nil {
			notificationDelegate.err = err
			return
		}
		notificationDelegate.instance = macSend(objc.ID(class), "new")
	})
	if notificationDelegate.err != nil || notificationDelegate.instance == 0 {
		p.setNotificationStatus("delegate_unavailable")
		return false
	}
	macSend(center, "setDelegate:", notificationDelegate.instance)
	return true
}

// UNUserNotificationCenter requires an app bundle identifier.
func (p *macNativePlatform) Notify(title, body string) {
	p.onMain(func() {
		bundle := macSend(macClass("NSBundle"), "mainBundle")
		if macSend(bundle, "bundleIdentifier") == 0 {
			p.setNotificationStatus("app_bundle_required")
			return
		}
		notificationFramework.Do(func() {
			_, err := purego.Dlopen("/System/Library/Frameworks/UserNotifications.framework/UserNotifications", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
			notificationFramework.ready = err == nil
		})
		if !notificationFramework.ready {
			p.setNotificationStatus("framework_unavailable")
			return
		}
		center := macSend(macClass("UNUserNotificationCenter"), "currentNotificationCenter")
		if !installNotificationDelegate(center, p) {
			return
		}
		completion := objc.NewBlock(func(_ objc.Block, granted bool, err objc.ID) {
			if err != 0 {
				p.setNotificationStatus("authorization_failed: " + macString(macSend(err, "localizedDescription")))
				return
			}
			if !granted {
				p.setNotificationStatus("permission_denied")
				return
			}
			p.onMain(func() {
				content := macSend(macClass("UNMutableNotificationContent"), "new")
				defer macSend(content, "release")
				macSend(content, "setTitle:", macText(title))
				macSend(content, "setBody:", macText(body))
				identifier := macSend(macSend(macClass("NSUUID"), "UUID"), "UUIDString")
				request := macSend(macClass("UNNotificationRequest"), "requestWithIdentifier:content:trigger:", identifier, content, objc.ID(0))
				delivered := objc.NewBlock(func(_ objc.Block, err objc.ID) {
					if err != 0 {
						p.setNotificationStatus("delivery_failed: " + macString(macSend(err, "localizedDescription")))
					} else {
						p.setNotificationStatus("scheduled")
					}
				})
				macSend(center, "addNotificationRequest:withCompletionHandler:", request, delivered)
				delivered.Release()
			})
		})
		macSend(center, "requestAuthorizationWithOptions:completionHandler:", uintptr(1|2|4), completion)
		completion.Release()
	})
}

func (p *macNativePlatform) setNotificationStatus(status string) { p.notificationStatus.Store(&status) }
