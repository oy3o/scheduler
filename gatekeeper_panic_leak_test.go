package scheduler

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"runtime"
)

type customPanicTask struct {
	msg string
}

func (p *customPanicTask) Priority() int { return 10 }
func (p *customPanicTask) Execute(ctx Context) error {
	panic(p.msg)
}

func TestPanicStackLeak(t *testing.T) {
	config := DefaultConfig()
	var onErrorErr error
	config.OnError = func(task Task, err error) {
		onErrorErr = err
	}

	g := New(config)

	ctx, cancel := context.WithCancel(context.Background())
	defer g.Wait()
	defer cancel()

	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	task := &customPanicTask{msg: "line1\nline2\ngoroutine"}
	g.Submit(task)

	time.Sleep(100 * time.Millisecond)

	if onErrorErr == nil {
		t.Fatalf("expected error from panic")
	}

	if strings.Contains(onErrorErr.Error(), "goroutine") {
		t.Fatalf("error contains stack trace or multiple lines: %s", onErrorErr.Error())
	}

	fmt.Printf("Error: %v\n", onErrorErr)
}
