package goui

import "runtime"

// macAutoreleasePool keeps creation, every Objective-C call using autoreleased
// objects, and pool draining on the same OS thread. Update runs on an unlocked
// Go goroutine; a sequence of native calls alone does not prevent migration.
// Call as defer macAutoreleasePool()() so draining precedes UnlockOSThread.
func macAutoreleasePool() func() {
	runtime.LockOSThread()
	pool := macSend(macClass("NSAutoreleasePool"), "new")
	return func() {
		defer runtime.UnlockOSThread()
		macSend(pool, "release")
	}
}
