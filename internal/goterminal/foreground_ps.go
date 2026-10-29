//go:build !darwin && !windows

package goterminal

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/SurTeam/Water/internal/goagent"
)

func sampleProcessGroups(ctx context.Context, groups []int) (map[int]goagent.Process, error) {
	pids := make([]string, len(groups))
	for i, group := range groups {
		pids[i] = strconv.Itoa(group)
	}
	command := exec.CommandContext(ctx, "ps", "-p", strings.Join(pids, ","), "-o", "pid=", "-o", "pgid=", "-o", "lstart=", "-o", "args=")
	// A descendant retaining stdout must not keep cancellation blocked indefinitely.
	command.WaitDelay = 100 * time.Millisecond
	output, err := command.Output()
	return parseForegroundOutput(output, err, ctx.Err())
}
