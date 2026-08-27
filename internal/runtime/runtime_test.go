package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jhovanic/godot-mcp-server/internal/audit"
)

// fakeAutoload stands in for the Godot-side autoload script's TCP
// listener: it's the server in this relationship, exactly as
// scripts/godot_operations.gd's counterpart would be inside a running
// Godot project. handle decides how to respond to each request line.
type fakeAutoload struct {
	ln net.Listener
}

func startFakeAutoload(t *testing.T, handle func(op string) response) *fakeAutoload {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeAutoload{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil {
					return
				}
				var req request
				if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &req); err != nil {
					return
				}
				resp := handle(req.Operation)
				_ = json.NewEncoder(conn).Encode(resp)
			}()
		}
	}()
	return f
}

func (f *fakeAutoload) port(t *testing.T) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(f.ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}
	return port
}

func TestClient_Ping(t *testing.T) {
	fake := startFakeAutoload(t, func(op string) response {
		if op != "ping" {
			return response{OK: false, Error: "unexpected op: " + op}
		}
		result, _ := json.Marshal("pong")
		return response{OK: true, Result: result}
	})

	var logBuf bytes.Buffer
	c := &Client{
		Port:   fake.port(t),
		Logger: audit.New(&logBuf),
		Dialer: net.Dialer{Timeout: 2 * time.Second},
	}

	result, err := c.Ping(t.Context())
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if result != "pong" {
		t.Fatalf("Ping result = %q, want %q", result, "pong")
	}

	if !strings.Contains(logBuf.String(), `"operation":"ping"`) {
		t.Errorf("audit log missing ping entry: %s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), `"tier":"runtime"`) {
		t.Errorf("audit log entry missing runtime tier: %s", logBuf.String())
	}
}

func TestClient_OperationFailure(t *testing.T) {
	fake := startFakeAutoload(t, func(op string) response {
		return response{OK: false, Error: "autoload not ready"}
	})

	var logBuf bytes.Buffer
	c := &Client{
		Port:   fake.port(t),
		Logger: audit.New(&logBuf),
		Dialer: net.Dialer{Timeout: 2 * time.Second},
	}

	_, err := c.Ping(t.Context())
	if err == nil {
		t.Fatal("Ping against a failing autoload, want error")
	}

	var entry audit.Entry
	if err := json.Unmarshal(bytes.TrimSpace(logBuf.Bytes()), &entry); err != nil {
		t.Fatalf("audit log entry is not valid JSON: %v (%s)", err, logBuf.String())
	}
	if entry.Outcome != audit.OutcomeError {
		t.Errorf("audit entry outcome = %q, want %q", entry.Outcome, audit.OutcomeError)
	}
}

func TestClient_OnlyDialsLoopback(t *testing.T) {
	// There is no field on Client for a host — this test documents that
	// invariant so a reviewer notices immediately if one gets added.
	c := &Client{Port: 12345}
	if got := c.addr(); got != "127.0.0.1:12345" {
		t.Fatalf("addr() = %q, want a 127.0.0.1 address", got)
	}
}

func TestClient_Unreachable(t *testing.T) {
	var logBuf bytes.Buffer
	// Port with nothing listening on it.
	c := &Client{
		Port:   1, // reserved, nothing should be listening
		Logger: audit.New(&logBuf),
		Dialer: net.Dialer{Timeout: 200 * time.Millisecond},
	}

	if _, err := c.Ping(t.Context()); err == nil {
		t.Fatal("Ping to an unreachable port, want error")
	}
}
