package scheduler

import (
	"errors"
	"fmt"
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

// PanicError is a hard error wrapper for a recovered panic.
// 🛡️ Sentinel: Preserves the raw panic payload for downstream type assertions
// instead of coercing it into a formatted string, while handling stack traces separately.
type PanicError struct {
	Payload any
	Stack   []byte
}

func (p *PanicError) Error() string {
	return fmt.Sprintf("task panicked: %v", p.Payload)
}
