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
// scripts/mcp_runtime_autoload.gd's counterpart would be inside a running
// Godot project. handle decides how to respond to each request.
type fakeAutoload struct {
	ln net.Listener
}

func startFakeAutoload(t *testing.T, handle func(req request) response) *fakeAutoload {
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
				resp := handle(req)
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

func jsonResult(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshaling fake result: %v", err)
	}
	return b
}

func TestClient_Ping(t *testing.T) {
	fake := startFakeAutoload(t, func(req request) response {
		if req.Operation != "ping" {
			return response{OK: false, Error: "unexpected op: " + req.Operation}
		}
		return response{OK: true, Result: jsonResult(t, "pong")}
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
	fake := startFakeAutoload(t, func(req request) response {
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

func TestClient_ReadRuntimeSceneTree(t *testing.T) {
	fake := startFakeAutoload(t, func(req request) response {
		if req.Operation != "read_scene_tree" {
			return response{OK: false, Error: "unexpected op: " + req.Operation}
		}
		node := RuntimeSceneNode{
			Name: "Main",
			Type: "Node",
			Path: ".",
			Children: []RuntimeSceneNode{
				{Name: "Player", Type: "CharacterBody2D", Path: "Player"},
			},
		}
		return response{OK: true, Result: jsonResult(t, node)}
	})

	var logBuf bytes.Buffer
	c := &Client{
		Port:   fake.port(t),
		Logger: audit.New(&logBuf),
		Dialer: net.Dialer{Timeout: 2 * time.Second},
	}

	node, err := c.ReadRuntimeSceneTree(t.Context())
	if err != nil {
		t.Fatalf("ReadRuntimeSceneTree: %v", err)
	}
	if node.Name != "Main" || len(node.Children) != 1 || node.Children[0].Path != "Player" {
		t.Fatalf("unexpected node: %+v", node)
	}
	if !strings.Contains(logBuf.String(), `"operation":"read_runtime_scene_tree"`) {
		t.Errorf("audit log missing read_runtime_scene_tree entry: %s", logBuf.String())
	}
}

func TestClient_ReadRuntimeNodeProperty(t *testing.T) {
	var gotParams runtimeNodePropertyParams
	fake := startFakeAutoload(t, func(req request) response {
		if req.Operation != "read_node_property" {
			return response{OK: false, Error: "unexpected op: " + req.Operation}
		}
		b, _ := json.Marshal(req.Params)
		_ = json.Unmarshal(b, &gotParams)
		return response{OK: true, Result: jsonResult(t, RuntimeNodePropertyResult{Value: "100", Type: "int"})}
	})

	var logBuf bytes.Buffer
	c := &Client{
		Port:   fake.port(t),
		Logger: audit.New(&logBuf),
		Dialer: net.Dialer{Timeout: 2 * time.Second},
	}

	result, err := c.ReadRuntimeNodeProperty(t.Context(), "Player", "health")
	if err != nil {
		t.Fatalf("ReadRuntimeNodeProperty: %v", err)
	}
	if result.Value != "100" || result.Type != "int" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if gotParams.NodePath != "Player" || gotParams.PropertyName != "health" {
		t.Fatalf("fake did not receive expected params: %+v", gotParams)
	}
}

func TestDiscoverInstances_FindsMultipleListeners(t *testing.T) {
	fakeA := startFakeAutoload(t, func(req request) response {
		if req.Operation != "hello" {
			return response{OK: false, Error: "unexpected op: " + req.Operation}
		}
		return response{OK: true, Result: jsonResult(t, map[string]string{"project_name": "demo-a"})}
	})
	fakeB := startFakeAutoload(t, func(req request) response {
		return response{OK: true, Result: jsonResult(t, map[string]string{"project_name": "demo-b"})}
	})

	portA := fakeA.port(t)
	portB := fakeB.port(t)
	lo, hi := portA, portB
	if lo > hi {
		lo, hi = hi, lo
	}
	// Widen the range slightly on each side so at least one genuinely
	// empty port is also scanned, proving the scan doesn't just stop at
	// the first live one.
	portRange := PortRange{Start: lo - 1, End: hi + 1}

	var logBuf bytes.Buffer
	instances, err := DiscoverInstances(t.Context(), portRange, audit.New(&logBuf))
	if err != nil {
		t.Fatalf("DiscoverInstances: %v", err)
	}
	if len(instances) != 2 {
		t.Fatalf("DiscoverInstances found %d instances, want 2: %+v", len(instances), instances)
	}
	byPort := map[int]string{instances[0].Port: instances[0].ProjectName, instances[1].Port: instances[1].ProjectName}
	if byPort[portA] != "demo-a" || byPort[portB] != "demo-b" {
		t.Fatalf("unexpected instances: %+v", instances)
	}
	if !strings.Contains(logBuf.String(), `"operation":"discover_runtime_instances"`) {
		t.Errorf("audit log missing discover_runtime_instances entry: %s", logBuf.String())
	}
}

func TestDiscoverInstances_EmptyRangeReturnsEmptyNotError(t *testing.T) {
	var logBuf bytes.Buffer
	// Reserved/low ports: nothing should ever be listening here.
	instances, err := DiscoverInstances(t.Context(), PortRange{Start: 2, End: 4}, audit.New(&logBuf))
	if err != nil {
		t.Fatalf("DiscoverInstances: %v", err)
	}
	if len(instances) != 0 {
		t.Fatalf("DiscoverInstances found %d instances, want 0: %+v", len(instances), instances)
	}
}
