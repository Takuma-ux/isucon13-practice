package main

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Subprocess timeouts use context.Background(), not the HTTP request context.
// Killing init.sh or pdnsutil early can leave DB/DNS partially updated; client
// disconnect must not cancel these (bench may time out waiting while init runs).
const (
	initSubprocessTimeout     = 120 * time.Second
	pdnsutilSubprocessTimeout = 30 * time.Second
)

func runSubprocess(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("subprocess %q: %w", name, ctx.Err())
	}
	return out, err
}
