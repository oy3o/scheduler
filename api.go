package scheduler

import (
	"errors"
	"fmt"
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

// PanicError wraps a raw panic payload and its corresponding stack trace.
// 🛡️ Sentinel: Ensures that the internal panic state is preserved for telemetry while
// exposing a sanitized string representation in the Error() method to prevent stack trace leakage.
type PanicError struct {
	Payload any
	Stack   []byte
}

func (p *PanicError) Error() string {
	pStr := fmt.Sprintf("%v", p.Payload)
	// 🛡️ Sentinel: Sanitize the public error to prevent stack trace leakage.
	// Strip everything after the first standard line-breaking character.
	if idx := strings.IndexAny(pStr, "\n\r\f\v"); idx != -1 {
		pStr = pStr[:idx]
	}
	return "task panicked: " + pStr
}

// Format implements fmt.Formatter to allow exposing the full stack trace for internal logging (e.g., via %+v).
func (p *PanicError) Format(f fmt.State, verb rune) {
	switch verb {
	case 'v':
		if f.Flag('+') {
			fmt.Fprintf(f, "task panicked: %v\n%s", p.Payload, p.Stack)
			return
		}
		fallthrough
	case 's':
		fmt.Fprintf(f, "%s", p.Error())
	case 'q':
		fmt.Fprintf(f, "%q", p.Error())
	}
}

// Unwrap provides access to the underlying payload if it's an error.
func (p *PanicError) Unwrap() error {
	if err, ok := p.Payload.(error); ok {
		return err
	}
	return nil
}
