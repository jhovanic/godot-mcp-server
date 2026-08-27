// Package audit provides per-invocation audit logging for every tool
// operation this server exposes.
//
// Per SECURITY.md, every tool invocation must be logged (operation,
// parameters, and result) independent of the MCP client's own logs. This
// package is that independent trail. It intentionally writes to a stream
// separate from MCP protocol traffic: the stdio transport uses stdout for
// JSON-RPC messages, so audit entries must never be written there.
package audit

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Outcome is the result classification of a logged operation.
type Outcome string

const (
	OutcomeOK    Outcome = "ok"
	OutcomeError Outcome = "error"
)

// Entry is a single audit record. Fields are exported so callers can shape
// Params/Result however suits the operation, as long as it's JSON-marshalable.
type Entry struct {
	Time       time.Time `json:"time"`
	Tier       string    `json:"tier"`      // e.g. "headless", "runtime"
	Operation  string    `json:"operation"` // fixed operation name, never derived from free-form input
	Params     any       `json:"params,omitempty"`
	Outcome    Outcome   `json:"outcome"`
	Result     any       `json:"result,omitempty"`
	Error      string    `json:"error,omitempty"`
	DurationMS int64     `json:"duration_ms"`
}

// Logger writes audit entries as newline-delimited JSON to an underlying
// writer. It is safe for concurrent use.
type Logger struct {
	mu sync.Mutex
	w  io.Writer
}

// New returns a Logger that writes entries to w. w is typically stderr
// and/or a dedicated audit-log file — never the stdio transport's stdout.
func New(w io.Writer) *Logger {
	return &Logger{w: w}
}

// Log writes entry as a single JSON line. Marshaling failures are written
// as a best-effort fallback line rather than silently dropped, since a
// missing audit entry is itself a security-relevant gap.
func (l *Logger) Log(entry Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, err := json.Marshal(entry)
	if err != nil {
		b, _ = json.Marshal(Entry{
			Time:      entry.Time,
			Tier:      entry.Tier,
			Operation: entry.Operation,
			Outcome:   OutcomeError,
			Error:     "audit: failed to marshal entry: " + err.Error(),
		})
	}
	b = append(b, '\n')
	_, _ = l.w.Write(b)
}

// LogResult is a convenience wrapper around Log for the common case: an
// operation ran, took some time, and either produced a result or failed.
// Call it unconditionally at the call site (not just on the error path) so
// every invocation — success or failure — leaves a trail.
func (l *Logger) LogResult(tier, operation string, params, result any, err error, start time.Time) {
	entry := Entry{
		Time:       start,
		Tier:       tier,
		Operation:  operation,
		Params:     params,
		DurationMS: time.Since(start).Milliseconds(),
		Outcome:    OutcomeOK,
	}
	if err != nil {
		entry.Outcome = OutcomeError
		entry.Error = err.Error()
	} else {
		entry.Result = result
	}
	l.Log(entry)
}
