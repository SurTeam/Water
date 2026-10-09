//go:build !darwin

package goui

import (
	"errors"
	"os/exec"
	"strings"
)

func (p *otherNativePlatform) ChooseFile(title string) (string, error) {
	for _, picker := range []struct {
		command string
		args    []string
	}{
		{"zenity", []string{"--file-selection", "--title=" + title}},
		{"kdialog", []string{"--getopenfilename", "", "*.pem *.crt *.cer *.key|PEM files", "--title", title}},
	} {
		executable, err := exec.LookPath(picker.command)
		if err != nil {
			continue
		}
		output, err := exec.Command(executable, picker.args...).Output()
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return "", nil
		}
		if err != nil {
			return "", errors.New("file picker failed")
		}
		return strings.TrimSpace(string(output)), nil
	}
	return "", errors.New("install zenity or kdialog to select local files")
}
