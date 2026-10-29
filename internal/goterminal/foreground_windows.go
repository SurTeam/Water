//go:build windows

package goterminal

import (
	"context"
	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/gopty"
	"time"
)

func sampleProcessGroups(ctx context.Context, groups []int) (map[int]goagent.Process, error) {
	result := make(map[int]goagent.Process)
	for _, pid := range groups {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		p, err := gopty.InspectProcess(ctx, pid)
		if err != nil {
			continue
		}
		result[pid] = goagent.Process{PID: pid, PGID: pid, Started: p.Started, Name: p.Name, Args: p.Args}
	}
	return result, ctx.Err()
}
func (t *Terminal) platformDirectory() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	return gopty.ProcessDirectory(ctx, t.processID()), true
}
