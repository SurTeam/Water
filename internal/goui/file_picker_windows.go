package goui

import (
	"fmt"
	"runtime"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var commonDialogs = windows.NewLazySystemDLL("comdlg32.dll")
var getOpenFileName = commonDialogs.NewProc("GetOpenFileNameW")
var commonDialogError = commonDialogs.NewProc("CommDlgExtendedError")

// openFileName follows OPENFILENAMEW, including the pointer-sized reserved fields.
type openFileName struct {
	Size                         uint32
	Owner, Instance              windows.Handle
	Filter, CustomFilter         *uint16
	MaxCustomFilter, FilterIndex uint32
	File                         *uint16
	MaxFile                      uint32
	FileTitle                    *uint16
	MaxFileTitle                 uint32
	InitialDirectory, Title      *uint16
	Flags                        uint32
	FileOffset, FileExtension    uint16
	DefaultExtension             *uint16
	CustomData                   uintptr
	Hook                         uintptr
	TemplateName                 *uint16
	Reserved                     unsafe.Pointer
	ReservedWord, ExtendedFlags  uint32
}

func (p *otherNativePlatform) ChooseFile(title string) (string, error) {
	// Common dialogs own a thread-local message loop for the duration of the call.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	buffer := make([]uint16, 32768)
	titleW, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return "", err
	}
	filter := utf16.Encode([]rune("PEM files (*.pem;*.crt;*.cer;*.key)\x00*.pem;*.crt;*.cer;*.key\x00All files\x00*.*\x00\x00"))
	dialog := openFileName{File: &buffer[0], MaxFile: uint32(len(buffer)), Title: titleW, Filter: &filter[0], FilterIndex: 1, Flags: 0x00080000 | 0x00001000 | 0x00000800 | 0x00000008}
	dialog.Size = uint32(unsafe.Sizeof(dialog))
	ok, _, _ := getOpenFileName.Call(uintptr(unsafe.Pointer(&dialog)))
	if ok == 0 {
		code, _, _ := commonDialogError.Call()
		if code != 0 {
			return "", fmt.Errorf("file picker failed (Windows error 0x%x)", code)
		}
		return "", nil
	}
	return windows.UTF16ToString(buffer), nil
}
