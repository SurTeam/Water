package ptyprocess

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

func identity(pid int) (string, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	// comm may contain spaces and parentheses; the final ')' ends it.
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return "", syscall.ESRCH
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) <= 19 {
		return "", syscall.ESRCH
	}
	return fields[19], nil // field 22: starttime in clock ticks since boot
}
