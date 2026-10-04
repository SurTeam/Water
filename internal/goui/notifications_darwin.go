package goui

import (
	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"sync"
)

var notificationFramework struct {
	sync.Once
	ready bool
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
		macSend(center, "requestAuthorizationWithOptions:completionHandler:", uintptr(4), completion)
		completion.Release()
	})
}

func (p *macNativePlatform) setNotificationStatus(status string) { p.notificationStatus.Store(&status) }
