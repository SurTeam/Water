//go:build windows

package gopty

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func preparePowerShell(cmd *exec.Cmd) {
	name := strings.ToLower(filepath.Base(cmd.Path))
	if name != "powershell.exe" && name != "pwsh.exe" {
		return
	}
	for _, arg := range cmd.Args[1:] {
		switch strings.ToLower(arg) {
		case "-nologo", "-noprofile", "-noexit":
		default:
			return
		}
	}
	prefix := filepath.Join(os.TempDir(), "water-powershell-cwd-")
	script := `$global:WaterOriginalPrompt=$function:prompt
$global:WaterCwdFile='` + strings.ReplaceAll(prefix, "'", "''") + `'+$PID+'-'+(Get-Process -Id $PID).StartTime.ToFileTimeUtc()+'.txt'
function global:prompt {
 try{[IO.File]::WriteAllText($global:WaterCwdFile,$PWD.ProviderPath)}catch{}
 if($global:WaterOriginalPrompt){& $global:WaterOriginalPrompt}else{'PS '+$PWD+'> '}
}
`
	cmd.Args = append(cmd.Args, "-NoExit", "-Command", script)
}
