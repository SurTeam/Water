// water-update is a private, headless installer shipped beside the GUI. It
// intentionally has no dependency on the window system or workspace server.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goupdate"
)

func main() {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"build_variant": gobuild.Variant, "version": gobuild.Version})
		return
	}
	if len(args) != 2 || args[0] != goupdate.InstallerArgument {
		fmt.Fprintln(os.Stderr, "water-update: this helper is launched by Water's in-app updater")
		os.Exit(1)
	}
	if err := goupdate.RunInstaller(args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "water-update:", err)
		os.Exit(1)
	}
}
