package scheduler

import (
	"errors"
	"fmt"
	"strings"
)

// ErrGateClosed signals that an operation was attempted after the Gatekeeper shut down.
var ErrGateClosed = errors.New("gatekeeper is closed")

// PanicError wraps a recovered panic payload and its stack trace.
//
// 🛡️ Sentinel: Prevent DoS via information disclosure by separating the raw payload
// and stack trace. Its Error method sanitizes the payload to avoid leaking stack traces
// if the payload itself contains multiple lines (e.g., a re-panicked error).
type PanicError struct {
	Payload any
	Stack   []byte
}

func (p *PanicError) Error() string {
	pStr := fmt.Sprintf("%v", p.Payload)
	if idx := strings.Index(pStr, "\n"); idx != -1 {
		pStr = pStr[:idx]
	}
	return fmt.Sprintf("task panicked: %s", pStr)
}

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
