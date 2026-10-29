package goupdate

import (
	"fmt"
	"os"
)

func processAlive(int) bool                 { return true }
func acquireGuard(string) (*os.File, error) { return nil, fmt.Errorf("unsupported") }
func inheritedGuard() (*os.File, error)     { return nil, fmt.Errorf("unsupported") }
func recoverVisible(*Plan) error            { return fmt.Errorf("unsupported") }
func recoverStale(string)                   {}
