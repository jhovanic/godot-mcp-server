// Process launch and output streaming: launch_project, read_runtime_output,
// stop_runtime.
//
// This is a genuinely different concern from runtime.go's TCP dial-client
// (it never talks to the autoload at all — plain OS process management),
// living in the same package for discoverability, and the first stateful,
// cross-call server state anywhere in this codebase: every other tool is a
// single self-contained call, but reading a launched process's output or
// stopping it later requires remembering it exists between separate tool
// calls. Manager is that memory.
//
// Raw stdout/stderr only works for a process this server itself launches —
// OS pipes only exist between a process and its direct parent, so an
// editor-launched session's output is categorically unreachable no matter
// what (that's exactly why the other half of this tier, runtime.go's
// discovery, uses a TCP socket instead: sockets don't have that
// restriction). Godot already writes script errors/exceptions to stderr as
// formatted text with no debugger attached (this project's own
// checkScriptParses already relies on that), so this alone covers a real
// fraction of "tell me what broke" for a self-launched reproduction.
package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jhovanic/godot-mcp-server/internal/audit"
	"github.com/jhovanic/godot-mcp-server/internal/validate"
)

// OutputLine is one captured line of a launched process's stdout or
// stderr, tagged with a monotonic sequence number so ReadRuntimeOutput can
// page through new lines since a previous call.
type OutputLine struct {
	Seq    int    `json:"seq"`
	Stream string `json:"stream"` // "stdout" or "stderr"
	Text   string `json:"text"`
}

// lineBuffer is a capped ring buffer of OutputLine entries, fed by the two
// lineWriters (one per stream) of a single launched process. Oldest lines
// are evicted once maxLines is exceeded, so a long-running session can't
// grow unbounded server-side memory.
type lineBuffer struct {
	mu          sync.Mutex
	lines       []OutputLine
	nextSeq     int
	maxLines    int
	evictedThru int // highest Seq ever evicted; 0 if nothing has been evicted yet
}

func newLineBuffer(maxLines int) *lineBuffer {
	// nextSeq starts at 1, not 0: since_cursor's documented "0 means read
	// from the start" default relies on a strict Seq > cursor comparison
	// in since() below, which would otherwise silently exclude the very
	// first line ever appended (it would be Seq 0, and 0 > 0 is false).
	return &lineBuffer{maxLines: maxLines, nextSeq: 1}
}

func (b *lineBuffer) append(stream, text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, OutputLine{Seq: b.nextSeq, Stream: stream, Text: text})
	b.nextSeq++
	if len(b.lines) > b.maxLines {
		b.evictedThru = b.lines[0].Seq
		b.lines = b.lines[1:]
	}
}

// since returns every buffered line with Seq > cursor, the cursor value to
// pass on the next call, and whether cursor referred to lines already
// evicted (so a polling caller knows it missed output rather than silently
// reading a gap).
func (b *lineBuffer) since(cursor int) (lines []OutputLine, newCursor int, truncated bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	truncated = cursor < b.evictedThru
	lines = make([]OutputLine, 0)
	for _, l := range b.lines {
		if l.Seq > cursor {
			lines = append(lines, l)
		}
	}
	return lines, b.nextSeq - 1, truncated
}

// lineWriter is an io.Writer that splits arbitrary, possibly-partial writes
// on newlines and appends complete lines to buf tagged with stream.
// Trailing data with no newline yet is held until a later Write completes
// it. Not safe for concurrent use by multiple goroutines against the same
// instance, which is fine here: os/exec gives stdout and stderr each their
// own dedicated copy goroutine, and each lineWriter instance is used by
// exactly one of those.
type lineWriter struct {
	stream  string
	buf     *lineBuffer
	pending strings.Builder
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.pending.Write(p)
	s := w.pending.String()
	for {
		idx := strings.IndexByte(s, '\n')
		if idx == -1 {
			break
		}
		line := strings.TrimSuffix(s[:idx], "\r")
		w.buf.append(w.stream, line)
		s = s[idx+1:]
	}
	w.pending.Reset()
	w.pending.WriteString(s)
	return len(p), nil
}

// launchedProcess tracks one process this server started.
type launchedProcess struct {
	cmd *exec.Cmd
	buf *lineBuffer

	mu       sync.Mutex
	exited   bool
	exitCode *int
}

// Manager tracks every process this server has launched, across separate
// tool calls, for the lifetime of this server process. Construct one with
// NewManager at startup and share it — it is safe for concurrent use.
type Manager struct {
	mu        sync.Mutex
	instances map[string]*launchedProcess
	nextID    int

	godotBin         string
	root             *validate.Root
	logger           *audit.Logger
	maxInstances     int
	maxBufferedLines int
}

// NewManager constructs a Manager. maxInstances caps concurrently *running*
// launches (LaunchProject refuses a new one past the cap); maxBufferedLines
// caps each instance's output ring buffer.
func NewManager(godotBin string, root *validate.Root, logger *audit.Logger, maxInstances, maxBufferedLines int) *Manager {
	return &Manager{
		instances:        make(map[string]*launchedProcess),
		godotBin:         godotBin,
		root:             root,
		logger:           logger,
		maxInstances:     maxInstances,
		maxBufferedLines: maxBufferedLines,
	}
}

// LaunchProjectParams are the parameters for the launch_project operation.
type LaunchProjectParams struct {
	// ScenePath is the .tscn path to launch, relative to the project
	// root. Nil means the project's own configured main scene — Godot
	// resolves that itself, so this never needs to parse project.godot.
	ScenePath *string `json:"scene_path,omitempty"`
	// Headless controls whether Godot launches with a visible window.
	// Nil defaults to true (headless), matching this project's
	// headless-first posture everywhere else and working in any
	// environment, including one with no display — set explicitly false
	// to launch visibly.
	Headless *bool `json:"headless,omitempty"`
}

// LaunchProjectResult confirms a launch.
type LaunchProjectResult struct {
	// RunID identifies this launch for ReadRuntimeOutput/StopRuntime
	// calls. Unique for this server process's lifetime only — not durable
	// across a restart.
	RunID string `json:"run_id"`
}

// LaunchProject starts a new Godot process (headless by default) and
// begins capturing its stdout/stderr into a capped ring buffer. The
// process outlives this call — it keeps running, and its output stays
// readable via ReadRuntimeOutput, until StopRuntime or this server itself
// shuts down (see Shutdown).
func (m *Manager) LaunchProject(ctx context.Context, params LaunchProjectParams) (*LaunchProjectResult, error) {
	start := time.Now()
	result, err := m.launchProject(params)
	m.logger.LogResult("runtime", "launch_project", params, result, err, start)
	return result, err
}

func (m *Manager) launchProject(params LaunchProjectParams) (*LaunchProjectResult, error) {
	args := []string{"--path", m.root.String()}
	headless := true
	if params.Headless != nil {
		headless = *params.Headless
	}
	if headless {
		args = append(args, "--headless")
	}
	if params.ScenePath != nil {
		absScenePath, err := m.root.Resolve(*params.ScenePath)
		if err != nil {
			return nil, fmt.Errorf("runtime: launch_project: %w", err)
		}
		if filepath.Ext(absScenePath) != ".tscn" {
			return nil, fmt.Errorf("runtime: launch_project: not a .tscn file: %s", *params.ScenePath)
		}
		relScenePath, err := filepath.Rel(m.root.String(), absScenePath)
		if err != nil {
			return nil, fmt.Errorf("runtime: launch_project: computing project-relative path: %w", err)
		}
		args = append(args, "res://"+filepath.ToSlash(relScenePath))
	}

	m.mu.Lock()
	running := 0
	for _, lp := range m.instances {
		lp.mu.Lock()
		if !lp.exited {
			running++
		}
		lp.mu.Unlock()
	}
	if running >= m.maxInstances {
		m.mu.Unlock()
		return nil, fmt.Errorf("runtime: launch_project: %d instances already running (max %d) — stop one first", running, m.maxInstances)
	}
	m.mu.Unlock()

	runID, err := m.launchCommand(m.godotBin, args)
	if err != nil {
		return nil, fmt.Errorf("runtime: launch_project: %w", err)
	}
	return &LaunchProjectResult{RunID: runID}, nil
}

// launchCommand is the Godot-specific-argv-agnostic seam: it starts name
// with args, wires up output capture, registers the instance, and returns
// its run_id. Split out from launchProject so tests can exercise the
// generic launch/capture/exit-detection lifecycle with an arbitrary test
// command instead of a real Godot binary — see manager_test.go.
func (m *Manager) launchCommand(name string, args []string) (string, error) {
	cmd := exec.Command(name, args...) //nolint:gosec // name/args are server config (godotBin) or already-Root.Resolve'd, never raw user input
	buf := newLineBuffer(m.maxBufferedLines)
	cmd.Stdout = &lineWriter{stream: "stdout", buf: buf}
	cmd.Stderr = &lineWriter{stream: "stderr", buf: buf}

	// Deliberately not exec.CommandContext(ctx, ...): the context behind a
	// single launch_project tool call ends the moment that call returns,
	// but the process itself must keep running across later, separate
	// read_runtime_output/stop_runtime calls.
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting %s: %w", name, err)
	}

	lp := &launchedProcess{cmd: cmd, buf: buf}

	m.mu.Lock()
	m.nextID++
	runID := fmt.Sprintf("run-%d", m.nextID)
	m.instances[runID] = lp
	m.mu.Unlock()

	go func() {
		_ = cmd.Wait()
		lp.mu.Lock()
		lp.exited = true
		if cmd.ProcessState != nil {
			code := cmd.ProcessState.ExitCode()
			lp.exitCode = &code
		}
		lp.mu.Unlock()
	}()

	return runID, nil
}

func (m *Manager) get(runID string) (*launchedProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lp, ok := m.instances[runID]
	if !ok {
		return nil, fmt.Errorf("runtime: no such run_id %q", runID)
	}
	return lp, nil
}

// ReadRuntimeOutputParams are the parameters for the read_runtime_output
// operation.
type ReadRuntimeOutputParams struct {
	RunID string `json:"run_id"`
	// SinceCursor is the cursor value from a previous read; 0 (the zero
	// value) reads from the very start.
	SinceCursor int `json:"since_cursor,omitempty"`
}

// ReadRuntimeOutputResult reports every buffered line since SinceCursor.
type ReadRuntimeOutputResult struct {
	Lines []OutputLine `json:"lines"`
	// Cursor is the value to pass as SinceCursor on the next call.
	Cursor        int  `json:"cursor"`
	ProcessExited bool `json:"process_exited"`
	ExitCode      *int `json:"exit_code,omitempty"`
	// Truncated is true when SinceCursor referred to lines already evicted
	// from the ring buffer — the caller missed some output, not just read
	// an empty gap.
	Truncated bool `json:"truncated,omitempty"`
}

// ReadRuntimeOutput reads a launched process's buffered stdout/stderr
// since a previous cursor. Read-only: never affects the running process.
func (m *Manager) ReadRuntimeOutput(_ context.Context, params ReadRuntimeOutputParams) (*ReadRuntimeOutputResult, error) {
	start := time.Now()
	result, err := m.readRuntimeOutput(params)
	m.logger.LogResult("runtime", "read_runtime_output", params, result, err, start)
	return result, err
}

func (m *Manager) readRuntimeOutput(params ReadRuntimeOutputParams) (*ReadRuntimeOutputResult, error) {
	lp, err := m.get(params.RunID)
	if err != nil {
		return nil, fmt.Errorf("runtime: read_runtime_output: %w", err)
	}
	lines, cursor, truncated := lp.buf.since(params.SinceCursor)
	lp.mu.Lock()
	exited := lp.exited
	exitCode := lp.exitCode
	lp.mu.Unlock()
	return &ReadRuntimeOutputResult{
		Lines:         lines,
		Cursor:        cursor,
		ProcessExited: exited,
		ExitCode:      exitCode,
		Truncated:     truncated,
	}, nil
}

// StopRuntimeParams are the parameters for the stop_runtime operation.
type StopRuntimeParams struct {
	RunID string `json:"run_id"`
}

// StopRuntimeResult confirms a stop request.
type StopRuntimeResult struct {
	RunID string `json:"run_id"`
	// AlreadyExited is true if the process had already stopped on its own
	// before this call — not treated as an error.
	AlreadyExited bool `json:"already_exited"`
}

// StopRuntime terminates a launched process with a hard kill
// (os.Process.Kill()) on every platform, not a graceful
// SIGTERM-then-wait-then-SIGKILL sequence — SIGTERM doesn't port cleanly
// to Windows, a real target in this project's cross-compiled release
// matrix, and a debugging tool asking the game to stop has no need to
// preserve in-game state. The instance stays in the registry (its
// buffered output stays readable) after stopping; there is no
// registry-entry cleanup in this version.
func (m *Manager) StopRuntime(_ context.Context, params StopRuntimeParams) (*StopRuntimeResult, error) {
	start := time.Now()
	result, err := m.stopRuntime(params)
	m.logger.LogResult("runtime", "stop_runtime", params, result, err, start)
	return result, err
}

func (m *Manager) stopRuntime(params StopRuntimeParams) (*StopRuntimeResult, error) {
	lp, err := m.get(params.RunID)
	if err != nil {
		return nil, fmt.Errorf("runtime: stop_runtime: %w", err)
	}
	lp.mu.Lock()
	exited := lp.exited
	lp.mu.Unlock()
	if exited {
		return &StopRuntimeResult{RunID: params.RunID, AlreadyExited: true}, nil
	}
	if err := lp.cmd.Process.Kill(); err != nil {
		return nil, fmt.Errorf("runtime: stop_runtime: %w", err)
	}
	return &StopRuntimeResult{RunID: params.RunID}, nil
}

// Shutdown kills every currently-running tracked process. Call this from
// the server's own shutdown path so an interrupted server doesn't leave
// orphaned game processes running.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, lp := range m.instances {
		lp.mu.Lock()
		exited := lp.exited
		lp.mu.Unlock()
		if !exited {
			_ = lp.cmd.Process.Kill()
		}
	}
}
