package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"
)

type panicLogSpoofTask struct {
	payload any
}

func (t *panicLogSpoofTask) Priority() int { return 100 }
func (t *panicLogSpoofTask) Execute(ctx Context) error {
	panic(t.payload)
}

func TestGatekeeper_PanicLogSpoofing(t *testing.T) {
	errCh := make(chan error, 1)

	cfg := DefaultConfig()
	cfg.OnError = func(task Task, err error) {
		errCh <- err
	}
	g := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Start(ctx)

	for !g.started.Load() {
		// Wait
	}

	payload := "initial\rspoofed"
	g.Submit(&panicLogSpoofTask{payload: payload})

	select {
	case err := <-errCh:
		errStr := err.Error()
		if strings.Contains(errStr, "\r") {
			t.Fatalf("Log spoofing vulnerability detected: \\r found in error string: %q", errStr)
		}
		if !strings.Contains(errStr, "initial") {
			t.Fatalf("Expected error to contain 'initial', got %q", errStr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for error")
	}
}
