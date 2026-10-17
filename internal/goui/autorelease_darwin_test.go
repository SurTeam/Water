package goui

import (
	"runtime"
	"sync"
	"testing"

	"github.com/ebitengine/purego"
)

func TestMacAutoreleasePoolStaysOnCreatingThread(t *testing.T) {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_LOCAL)
	if err != nil {
		t.Fatal(err)
	}
	defer purego.Dlclose(lib)
	var threadSelf func() uintptr
	purego.RegisterLibFunc(&threadSelf, lib, "pthread_self")
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 100 {
				func() {
					defer macAutoreleasePool()()
					thread := threadSelf()
					// Native pools are thread-local, unlike Go goroutines. Force
					// scheduling while autoreleased objects are still in use.
					text := macText("啊啊啊啊啊啊啊")
					for range 4 {
						runtime.Gosched()
						if threadSelf() != thread {
							t.Error("autorelease pool migrated between OS threads")
							return
						}
						if macString(text) != "啊啊啊啊啊啊啊" {
							t.Error("autoreleased text changed during scheduling")
						}
					}
				}()
			}
		})
	}
	workers.Wait()
}
