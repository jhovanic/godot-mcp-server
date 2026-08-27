// Package runtime implements the TCP runtime tier client: it talks to an
// autoload script running *inside* the target Godot project, which listens
// on a localhost-only socket for read/interact commands against a live game
// or editor session (see README.md's architecture section).
//
// godot-mcp-server is the client here, not the listener — the autoload
// script owns the bound socket. This package only ever dials
// 127.0.0.1:<port>; there is no field or flag anywhere in this package that
// accepts a different host, per SECURITY.md's "No non-local network
// exposure by default" constraint.
//
// No operations are wired up as MCP tools yet — this is scaffolding for the
// tier described in CLAUDE.md/FEATURES.md. Ping is provided as a minimal,
// project-state-free reachability check so the client and its wire protocol
// are exercisable and testable ahead of any real live-state operation being
// added.
package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jhovanic/godot-mcp-server/internal/audit"
)

// loopbackHost is the only host this tier will ever dial. It is a package
// constant, not a config field, so pointing it at a non-loopback address
// can't be introduced by a config typo or a future flag.
const loopbackHost = "127.0.0.1"

// request is the fixed envelope for a single newline-delimited JSON message
// sent to the autoload's listener. Operation is a dispatch key only.
type request struct {
	Operation string `json:"operation"`
}

type response struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// Client talks to the autoload script's TCP listener inside a running Godot
// project.
type Client struct {
	// Port the autoload script is listening on. Host is always
	// loopbackHost and is not configurable.
	Port int
	// Logger receives an audit entry for every request, per SECURITY.md's
	// per-invocation logging requirement.
	Logger *audit.Logger
	// Dialer controls connection timeouts. The zero value is fine for
	// normal use; tests set a short Timeout.
	Dialer net.Dialer
}

func (c *Client) addr() string {
	return fmt.Sprintf("%s:%d", loopbackHost, c.Port)
}

// Ping performs a minimal reachability check against the autoload's
// listener. It reads no project or game state.
func (c *Client) Ping(ctx context.Context) (string, error) {
	start := time.Now()
	var result string
	err := c.call(ctx, "ping", &result)
	c.Logger.LogResult("runtime", "ping", nil, result, err, start)
	return result, err
}

// call sends a single fixed operation name (never free-form/user-supplied
// text interpreted as code) and decodes the structured JSON response.
func (c *Client) call(ctx context.Context, operation string, out any) error {
	conn, err := c.Dialer.DialContext(ctx, "tcp", c.addr())
	if err != nil {
		return fmt.Errorf("runtime: dialing %s: %w", c.addr(), err)
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	if err := json.NewEncoder(conn).Encode(request{Operation: operation}); err != nil {
		return fmt.Errorf("runtime: writing request: %w", err)
	}

	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("runtime: reading response: %w", err)
	}

	var resp response
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
		return fmt.Errorf("runtime: decoding response: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("runtime: operation %q failed: %s", operation, resp.Error)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("runtime: decoding result: %w", err)
		}
	}
	return nil
}
