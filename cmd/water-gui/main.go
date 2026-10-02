package main

import (
	"fmt"
	"os"

	"github.com/SurTeam/Water/internal/gouiapp"
)

func main() {
	if err := gouiapp.Run(os.Args[1:], "dev"); err != nil {
		fmt.Fprintln(os.Stderr, "water-gui:", err)
		os.Exit(1)
	}
}
