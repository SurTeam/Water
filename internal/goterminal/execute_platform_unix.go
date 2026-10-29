//go:build !windows

package goterminal

import "context"

func (t *Terminal) executePlatform(ctx context.Context, opts ExecuteOptions) (ExecuteResult, bool, error) {
	return ExecuteResult{}, false, nil
}
