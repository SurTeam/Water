//go:build windows

package goplatform

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"
)

// MkdirPrivate creates the sensitive directory with its final protected ACL,
// rather than creating a broadly inherited directory and repairing it later.
func MkdirPrivate(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	err = windows.CreateDirectory(name, &sa)
	runtime.KeepAlive(sd)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || !IsPrivate(path, info) {
		return errors.New("directory must have a private Windows ACL")
	}
	return nil
}

// POSIX permission bits do not describe a Windows DACL. Accept grants only to
// this user, SYSTEM and local administrators; reject null/unrecognized ACLs.
func IsPrivate(path string, info os.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	defer runtime.KeepAlive(sd)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	allowed := func(sid *windows.SID) bool {
		return sid != nil && (sid.Equals(user.User.Sid) || sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid))
	}
	owner, _, err := sd.Owner()
	if err != nil || !allowed(owner) {
		return false
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return false
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil {
			return false
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			if ace.Mask != 0 && !allowed((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
