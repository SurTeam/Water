package goplatform

import (
	"golang.org/x/sys/windows"
	"os/exec"
	"syscall"
	"unsafe"
)

// Detached children must outlive the launcher and must not allocate a console.
func Detach(command *exec.Cmd) {
	flags := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	// OpenSSH's job kills descendants when a command disconnects. Break away
	// only when the containing job permits it; other desktop launchers may not.
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), nil) == nil && limits.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0 {
		flags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
}
