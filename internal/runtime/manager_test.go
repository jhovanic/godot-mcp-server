package runtime

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jhovanic/godot-mcp-server/internal/audit"
	"github.com/jhovanic/godot-mcp-server/internal/validate"
)

// These tests exercise Manager's generic launch/capture/exit-detection
// lifecycle without needing a real Godot binary, using Go's standard
// re-exec-the-test-binary pattern (TestHelperProcess below): launchCommand
// is the seam that lets a test substitute an arbitrary command for
// launchProject's Godot-specific argv construction. Real-Godot coverage of
// the actual launch_project/read_runtime_output/stop_runtime tool trio
// lives in manager_real_godot_test.go.

// TestHelperProcess is not a real test — it's a re-exec target, only ever
// invoked as a subprocess with GO_WANT_HELPER_PROCESS=1 set. Without that
// variable it's a silent no-op, so a normal `go test` run doesn't treat it
// as a test case of its own.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	fmt.Println("stdout line 1")
	fmt.Fprintln(os.Stderr, "stderr line 1")
	fmt.Println("stdout line 2")
	if ms := os.Getenv("GO_HELPER_SLEEP_MS"); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil {
			time.Sleep(time.Duration(n) * time.Millisecond)
		}
	}
	os.Exit(0)
}

func newTestManager(t *testing.T, maxInstances, maxBufferedLines int) *Manager {
	t.Helper()
	root, err := validate.NewRoot(t.TempDir())
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	return NewManager("unused-in-these-tests", root, audit.New(discardWriter{}), maxInstances, maxBufferedLines)
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// launchHelper re-execs this test binary into TestHelperProcess above,
// setting sleepMS (0 = don't sleep, exit immediately after printing).
func launchHelper(t *testing.T, m *Manager, sleepMS int) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	if sleepMS > 0 {
		t.Setenv("GO_HELPER_SLEEP_MS", strconv.Itoa(sleepMS))
	}

	runID, err := m.launchCommand(self, []string{"-test.run=TestHelperProcess"})
	if err != nil {
		t.Fatalf("launchCommand: %v", err)
	}
	return runID
}

// waitFor polls cond every 20ms for up to 5s, failing the test if it never
// becomes true. Used instead of a fixed sleep since the helper process's
// exit timing isn't perfectly deterministic under test-runner load. For
// real-Godot tests (engine startup, not a near-instant re-exec'd Go
// helper), use waitForTimeout with a longer budget instead.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	waitForTimeout(t, 5*time.Second, cond)
}

func waitForTimeout(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition did not become true within %s", timeout)
}

func waitForExit(t *testing.T, m *Manager, runID string) *ReadRuntimeOutputResult {
	t.Helper()
	var result *ReadRuntimeOutputResult
	waitFor(t, func() bool {
		var err error
		result, err = m.ReadRuntimeOutput(context.Background(), ReadRuntimeOutputParams{RunID: runID})
		if err != nil {
			t.Fatalf("ReadRuntimeOutput: %v", err)
		}
		return result.ProcessExited
	})
	return result
}

func TestManager_LaunchCommand_CapturesOutputByStream(t *testing.T) {
	m := newTestManager(t, 4, 100)
	runID := launchHelper(t, m, 0)

	result := waitForExit(t, m, runID)

	var stdoutLines, stderrLines []string
	for _, l := range result.Lines {
		if l.Stream == "stdout" {
			stdoutLines = append(stdoutLines, l.Text)
		} else {
			stderrLines = append(stderrLines, l.Text)
		}
	}
	if len(stdoutLines) != 2 || stdoutLines[0] != "stdout line 1" || stdoutLines[1] != "stdout line 2" {
		t.Errorf("stdout lines = %v, want [stdout line 1, stdout line 2]", stdoutLines)
	}
	if len(stderrLines) != 1 || stderrLines[0] != "stderr line 1" {
		t.Errorf("stderr lines = %v, want [stderr line 1]", stderrLines)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", result.ExitCode)
	}
}

func TestManager_ReadRuntimeOutput_CursorPagination(t *testing.T) {
	m := newTestManager(t, 4, 100)
	runID := launchHelper(t, m, 0)

	var first *ReadRuntimeOutputResult
	waitFor(t, func() bool {
		var err error
		first, err = m.ReadRuntimeOutput(context.Background(), ReadRuntimeOutputParams{RunID: runID})
		if err != nil {
			t.Fatalf("ReadRuntimeOutput: %v", err)
		}
		return len(first.Lines) > 0
	})

	second, err := m.ReadRuntimeOutput(context.Background(), ReadRuntimeOutputParams{RunID: runID, SinceCursor: first.Cursor})
	if err != nil {
		t.Fatalf("ReadRuntimeOutput (second): %v", err)
	}
	for _, l := range second.Lines {
		for _, prev := range first.Lines {
			if l.Seq == prev.Seq {
				t.Fatalf("second read re-returned already-read line %+v", l)
			}
		}
	}
}

func TestManager_ReadRuntimeOutput_UnknownRunID(t *testing.T) {
	m := newTestManager(t, 4, 100)
	if _, err := m.ReadRuntimeOutput(context.Background(), ReadRuntimeOutputParams{RunID: "does-not-exist"}); err == nil {
		t.Fatal("ReadRuntimeOutput with an unknown run_id, want error")
	}
}

func TestManager_StopRuntime_KillsRunningProcess(t *testing.T) {
	m := newTestManager(t, 4, 100)
	runID := launchHelper(t, m, 5000)

	stopResult, err := m.StopRuntime(context.Background(), StopRuntimeParams{RunID: runID})
	if err != nil {
		t.Fatalf("StopRuntime: %v", err)
	}
	if stopResult.AlreadyExited {
		t.Error("StopRuntime reported AlreadyExited=true for a process that was still running")
	}

	waitForExit(t, m, runID)
}

func TestManager_StopRuntime_AlreadyExited(t *testing.T) {
	m := newTestManager(t, 4, 100)
	runID := launchHelper(t, m, 0)
	waitForExit(t, m, runID)

	stopResult, err := m.StopRuntime(context.Background(), StopRuntimeParams{RunID: runID})
	if err != nil {
		t.Fatalf("StopRuntime: %v", err)
	}
	if !stopResult.AlreadyExited {
		t.Error("StopRuntime did not report AlreadyExited=true for a process that had already exited")
	}
}

func TestManager_LaunchProject_RejectsPastMaxInstances(t *testing.T) {
	m := newTestManager(t, 1, 100)
	launchHelper(t, m, 5000)

	// launchProject (not launchCommand) is what enforces maxInstances, so
	// exercise it via the real entry point this time. It'll try to run
	// "unused-in-these-tests" as godotBin and fail to start — but the cap
	// check happens before that, so this must fail with the cap message,
	// not a "failed to start" error.
	_, err := m.LaunchProject(context.Background(), LaunchProjectParams{})
	if err == nil {
		t.Fatal("LaunchProject past maxInstances, want error")
	}
	if got := err.Error(); !strings.Contains(got, "already running") {
		t.Fatalf("LaunchProject error = %q, want it to mention the running-instance cap", got)
	}
}

func TestManager_Shutdown_KillsTrackedProcesses(t *testing.T) {
	m := newTestManager(t, 4, 100)
	runID := launchHelper(t, m, 5000)

	m.Shutdown()

	waitForExit(t, m, runID)
}

func TestLineBuffer_EvictsOldestAndReportsTruncated(t *testing.T) {
	b := newLineBuffer(2)
	b.append("stdout", "one")
	b.append("stdout", "two")
	b.append("stdout", "three") // evicts "one" (seq 0)

	lines, cursor, truncated := b.since(0)
	if !truncated {
		t.Error("since(0) after eviction, want Truncated=true")
	}
	if len(lines) != 2 || lines[0].Text != "two" || lines[1].Text != "three" {
		t.Errorf("lines = %+v, want [two, three]", lines)
	}
	if cursor != 3 {
		t.Errorf("cursor = %d, want 3", cursor)
	}
}

func TestLineWriter_SplitsPartialWritesAcrossCalls(t *testing.T) {
	b := newLineBuffer(100)
	w := &lineWriter{stream: "stdout", buf: b}

	_, _ = w.Write([]byte("hello "))
	_, _ = w.Write([]byte("world\nsecond li"))
	_, _ = w.Write([]byte("ne\n"))

	lines, _, _ := b.since(-1)
	if len(lines) != 2 || lines[0].Text != "hello world" || lines[1].Text != "second line" {
		t.Fatalf("lines = %+v, want [hello world, second line]", lines)
	}
}

func TestLineWriter_HoldsTrailingPartialLine(t *testing.T) {
	b := newLineBuffer(100)
	w := &lineWriter{stream: "stdout", buf: b}

	_, _ = w.Write([]byte("complete line\nno newline yet"))

	lines, _, _ := b.since(-1)
	if len(lines) != 1 || lines[0].Text != "complete line" {
		t.Fatalf("lines = %+v, want just [complete line] (trailing partial not yet flushed)", lines)
	}
}
