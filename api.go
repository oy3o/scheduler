package scheduler

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrGateClosed signals that an operation was attempted after the Gatekeeper shut down.
var ErrGateClosed = errors.New("gatekeeper is closed")

// Priority levels for tasks.
const (
	PriorityLow    = 0
	PriorityNormal = 100
	PriorityHigh   = 500
	PriorityUltra  = 1000
)

// Task is the fundamental unit of execution in the Ouroboros scheduler.
//
// Philosophy (Invisible Spine): The scheduler provides the structural boundary (Context)
// but refuses to intrude on business logic. Implementations own their lifecycle entirely.
// By pushing panics, cleanup, and resource release to Go's native defer within Execute,
// we cleanly decouple the Orchestrator (Gatekeeper) from the chaotic entropy of user code.
type Task interface {
	Priority() int
	Execute(ctx Context) error
}

// Context is a stack-allocated value type that provides task scheduling feedback.
type Context struct {
	state   *taskState
	version uint32
}

// PanicError wraps a recovered panic payload and its stack trace.
// It implements custom error formatting to sanitize public output
// while preserving full details for internal telemetry.
type PanicError struct {
	Payload any
	Stack   []byte
}

func (e *PanicError) Error() string {
	pStr := fmt.Sprintf("%v", e.Payload)
	if idx := strings.IndexAny(pStr, "\n\r\f\v"); idx != -1 {
		pStr = pStr[:idx]
	}
	return fmt.Sprintf("task panicked: %s", pStr)
}

func (e *PanicError) Format(s fmt.State, verb rune) {
	switch verb {
	case 'v':
		if s.Flag('+') {
			fmt.Fprintf(s, "task panicked: %v\n%s", e.Payload, e.Stack)
			return
		}
		fallthrough
	case 's':
		io.WriteString(s, e.Error())
	case 'q':
		fmt.Fprintf(s, "%q", e.Error())
	}
}

func (e *PanicError) Unwrap() error {
	if err, ok := e.Payload.(error); ok {
		return err
	}
	return nil
}
