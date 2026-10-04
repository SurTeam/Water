//go:build !darwin && !linux

package goui

import (
	"fmt"
	"os"
)

func mapNativeFontTable(_ *os.File, _ int64, _ int) ([]byte, func(), error) {
	return nil, nil, fmt.Errorf("font mapping unavailable")
}
