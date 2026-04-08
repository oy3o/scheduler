package scheduler

import (
	"errors"
	"fmt"
	"strings"
)

// PanicError wraps a recovered panic payload and its stack trace.
// It securely sanitizes the payload when formatting the public error message
// to prevent multi-line nested stack traces from leaking to public APIs.
type PanicError struct {
	Payload any
	Stack   []byte
}

func (p *PanicError) Error() string {
	pStr := fmt.Sprintf("%v", p.Payload)
	// 🛡️ Sentinel: Prevent stack trace leakage in public APIs.
	// We truncate the panic payload at the first occurrence of any standard
	// line-breaking character to prevent nested stack traces from leaking
	// via alternate whitespace formats.
	if idx := strings.IndexAny(pStr, "\n\r\f\v"); idx != -1 {
		pStr = pStr[:idx]
	}
	return fmt.Sprintf("task panicked: %s", pStr)
}

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
