package goterminal

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Zsh does not bind xterm's forward-delete sequence by default. Install a
// one-shot precmd hook after the user's startup files, preserving custom keys.
// The shim restores ZDOTDIR immediately, so all other startup files and child
// shells retain their usual locations. Explicit -f/-c shells remain untouched.
func prepareShellIntegration(cmd *exec.Cmd, program string, args []string) (string, error) {
	if filepath.Base(program) != "zsh" {
		return "", nil
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--no-rcs" || strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsAny(arg, "fc") {
			return "", nil
		}
	}
	dir, err := os.MkdirTemp("", "water-zsh-")
	if err != nil {
		return "", err
	}
	shim := `# Water terminal key integration; no user startup files are changed.
if [[ ${WATER_ORIGINAL_ZDOTDIR_SET} == 1 ]]; then
  export ZDOTDIR=$WATER_ORIGINAL_ZDOTDIR
else
  unset ZDOTDIR
fi
unset WATER_ORIGINAL_ZDOTDIR WATER_ORIGINAL_ZDOTDIR_SET
[[ ! -r ${ZDOTDIR:-$HOME}/.zshenv ]] || source ${ZDOTDIR:-$HOME}/.zshenv
if [[ -o interactive ]]; then
  _water_terminal_keys() {
    local water_map water_binding
    for water_map in emacs viins; do
      water_binding=$(bindkey -M $water_map $'\e[3~' 2>/dev/null)
      if [[ $water_binding == *undefined-key ]]; then
        bindkey -M $water_map $'\e[3~' delete-char
      fi
    done
    precmd_functions=(${precmd_functions:#_water_terminal_keys})
    unfunction _water_terminal_keys
  }
  precmd_functions+=(_water_terminal_keys)
fi
`
	if err := os.WriteFile(filepath.Join(dir, ".zshenv"), []byte(shim), 0600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	value, set := os.LookupEnv("ZDOTDIR")
	setValue := "0"
	if set {
		setValue = "1"
	}
	cmd.Env = append(cmd.Env, "WATER_ORIGINAL_ZDOTDIR="+value, "WATER_ORIGINAL_ZDOTDIR_SET="+setValue, "ZDOTDIR="+dir)
	return dir, nil
}
