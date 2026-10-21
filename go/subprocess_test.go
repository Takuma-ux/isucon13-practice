package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunSubprocessCompletes(t *testing.T) {
	out, err := runSubprocess(5*time.Second, "true")
	if err != nil {
		t.Fatalf("true: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestRunSubprocessTimeoutFires(t *testing.T) {
	start := time.Now()
	_, err := runSubprocess(200*time.Millisecond, "sleep", "30")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("timeout took too long (%s); subprocess may not have been killed", elapsed)
	}
}
