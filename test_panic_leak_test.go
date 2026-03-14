package scheduler

import (
	"context"
	"strings"
	"testing"
)

func TestFuture_PanicNoLeak(t *testing.T) {
	g := New(DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go g.Start(ctx)
	for !g.started.Load() {
	}

	f, _ := SubmitFunc(g, PriorityNormal, func(ctx Context) (int, error) {
		panic("test panic")
	})

	_, err := f.Get(context.Background())
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	errStr := err.Error()
	if strings.Contains(errStr, "debug.Stack") || strings.Contains(errStr, "goroutine") {
		t.Fatalf("Stack trace leaked in error: %v", errStr)
	}

	if !strings.Contains(errStr, "test panic") {
		t.Fatalf("Error should contain original panic message: %v", errStr)
	}
}
