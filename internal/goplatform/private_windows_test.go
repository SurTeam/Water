//go:build windows

package goplatform

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsPrivateACLAndInheritedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private")
	if err := MkdirPrivate(path); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(path, "secret")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, file} {
		info, err := os.Lstat(p)
		if err != nil || !IsPrivate(p, info) {
			t.Fatalf("private ACL rejected %s: %v", p, err)
		}
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetNamedSecurityInfo(file, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Lstat(file)
	if IsPrivate(file, info) {
		t.Fatal("accepted Everyone grant")
	}
}
