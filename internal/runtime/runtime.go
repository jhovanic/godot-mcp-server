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
// The autoload binds the first free port in a fixed, documented range
// (DefaultPortRange) rather than one single port: something else might
// already be bound to the "obvious" choice, and Godot's own "run multiple
// instances" feature means more than one live listener can legitimately
// exist at once (local multiplayer testing). So this side can't just dial
// "the" port — DiscoverInstances scans the range and returns every port
// that responds, letting the caller pick which live instance it means.
//
// This package is entirely stateless: every read here is a fresh,
// independent TCP dial, unlike this tier's other half (see manager.go),
// which tracks long-running launched processes across separate calls.
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

// PortRange is the fixed, documented range of loopback ports the autoload
// script tries to bind at _ready() (see scripts/mcp_runtime_autoload.gd)
// and the range DiscoverInstances scans for live listeners. Both sides must
// agree on this value: an editor-launched session receives no
// runtime-negotiated value from this server at all, so the range is a
// convention, not something dialed out dynamically. Configurable on each
// side independently (this server's -runtime-port-range flag; an exported
// constant in the autoload template) — if an operator changes one, they
// must change the other to match.
type PortRange struct {
	Start, End int
}

// DefaultPortRange is the convention both this client and the shipped
// autoload template use unless explicitly reconfigured. Ten ports, chosen
// to avoid Godot's own remote-debug/language-server ports (6005-6008) and
// the common 8080 dev-server collision zone.
var DefaultPortRange = PortRange{Start: 9080, End: 9089}

// discoverDialTimeout bounds how long DiscoverInstances waits on each
// candidate port before concluding nothing is listening there. Short,
// since most ports in the range are expected to be empty on any given scan
// (ten dials at this timeout is at most ~1.5s total, not per-port
// noticeable).
const discoverDialTimeout = 150 * time.Millisecond

// Instance describes one live autoload listener found during discovery.
type Instance struct {
	Port int `json:"port"`
	// ProjectName is the responding project's own configured name (read
	// from its ProjectSettings), for disambiguating multiple listeners —
	// either several simultaneous instances of the same project (local
	// multiplayer testing) or, in principle, different projects on the
	// same machine.
	ProjectName string `json:"project_name"`
}

// DiscoverInstances probes every port in portRange with a short-timeout
// dial and returns every one that answers — not just the first. Multiple
// simultaneous instances are a real, expected result, not an error; the
// caller picks which one it means for any follow-up read. A scan that
// finds nothing is a normal, successful result (there may genuinely be
// nothing running) — see internal/tools's registerDiscoverRuntimeInstances
// for the accompanying hint text when the list comes back empty.
func DiscoverInstances(ctx context.Context, portRange PortRange, logger *audit.Logger) ([]Instance, error) {
	start := time.Now()
	instances := make([]Instance, 0)
	for port := portRange.Start; port <= portRange.End; port++ {
		probe := &Client{Port: port, Dialer: net.Dialer{Timeout: discoverDialTimeout}}
		info, err := probe.hello(ctx)
		if err != nil {
			continue // nothing listening here (or it errored) — not fatal to the scan
		}
		instances = append(instances, *info)
	}
	logger.LogResult("runtime", "discover_runtime_instances", portRange, instances, nil, start)
	return instances, nil
}

// request is the fixed envelope for a single newline-delimited JSON message
// sent to the autoload's listener. Operation is a dispatch key only, never
// interpreted as code — the autoload's own match statement mirrors
// scripts/godot_operations.gd's "fixed, named operations only" discipline.
type request struct {
	Operation string `json:"operation"`
	Params    any    `json:"params,omitempty"`
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
	// loopbackHost and is not configurable. Found via DiscoverInstances.
	Port int
	// Logger receives an audit entry for every request, per SECURITY.md's
	// per-invocation logging requirement. May be left nil for internal,
	// non-tool-facing probes (see hello, used only by DiscoverInstances,
	// which logs its own single summarizing entry instead of one per
	// probed port).
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
	err := c.call(ctx, "ping", nil, &result)
	c.Logger.LogResult("runtime", "ping", nil, result, err, start)
	return result, err
}

// hello is the internal, unlogged probe DiscoverInstances uses to test
// reachability and fetch identifying metadata in one round trip. It is
// deliberately separate from the public, audit-logged Ping/read operations
// below: a discovery scan dials up to PortRange's full width per call, and
// logging one audit entry per empty port would drown the one entry that
// actually matters (DiscoverInstances' own summarizing log line).
func (c *Client) hello(ctx context.Context) (*Instance, error) {
	var result struct {
		ProjectName string `json:"project_name"`
	}
	if err := c.call(ctx, "hello", nil, &result); err != nil {
		return nil, err
	}
	return &Instance{Port: c.Port, ProjectName: result.ProjectName}, nil
}

// RuntimeSceneNode is one node in a *live* scene tree, as reported by a
// running instance's autoload. Same shape as headless.SceneNode
// (name/type/children) plus Path — the node's address relative to the
// current scene root, in Godot's own NodePath syntax — so a result here
// can be chained directly into ReadRuntimeNodeProperty without the caller
// reconstructing paths by hand. headless.SceneNode itself is left
// unchanged; this is a new, independent type for a new, independent
// operation.
type RuntimeSceneNode struct {
	Name     string             `json:"name"`
	Type     string             `json:"type"`
	Path     string             `json:"path"`
	Children []RuntimeSceneNode `json:"children,omitempty"`
}

// ReadRuntimeSceneTree returns the live scene tree of the process this
// Client's Port is bound to (see DiscoverInstances for finding a Port).
// Read-only: does not modify the running game's state.
func (c *Client) ReadRuntimeSceneTree(ctx context.Context) (*RuntimeSceneNode, error) {
	start := time.Now()
	var node RuntimeSceneNode
	err := c.call(ctx, "read_scene_tree", nil, &node)
	c.Logger.LogResult("runtime", "read_runtime_scene_tree", nil, node, err, start)
	if err != nil {
		return nil, err
	}
	return &node, nil
}

// runtimeNodePropertyParams addresses a node the same way every other
// node_path field in this project does: relative to the scene root
// (here, get_tree().current_scene on the autoload side), empty string
// meaning the root itself.
type runtimeNodePropertyParams struct {
	NodePath     string `json:"node_path"`
	PropertyName string `json:"property_name"`
}

// RuntimeNodePropertyResult reports a live property's value as a string
// representation (str(value) on the GDScript side), not a fully
// round-trippable typed value — mirroring SetNodePropertyResult's own
// PreviousValue field, which is str()'d for the same reason: this exists
// so an AI caller can understand what a value currently is, not so it can
// be fed back into another structured call. Type names the value's runtime
// class (e.g. "Vector2", "int", "String") so the caller isn't left
// guessing what kind of thing the string represents.
type RuntimeNodePropertyResult struct {
	Value string `json:"value"`
	Type  string `json:"type"`
}

// ReadRuntimeNodeProperty reads a single live property value off a running
// node addressed by nodePath (relative to the current scene root; empty
// string means the root itself). Read-only.
func (c *Client) ReadRuntimeNodeProperty(ctx context.Context, nodePath, propertyName string) (*RuntimeNodePropertyResult, error) {
	start := time.Now()
	params := runtimeNodePropertyParams{NodePath: nodePath, PropertyName: propertyName}
	var result RuntimeNodePropertyResult
	err := c.call(ctx, "read_node_property", params, &result)
	c.Logger.LogResult("runtime", "read_runtime_node_property", params, result, err, start)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// Discoverer is the stateless, tool-facing entry point for the
// discover_runtime_instances/read_runtime_scene_tree/
// read_runtime_node_property tools. Unlike Manager, it holds no per-call
// state: each method either scans the port range fresh or constructs a
// short-lived Client for the one port it's asked about.
type Discoverer struct {
	PortRange PortRange
	Logger    *audit.Logger
}

// DiscoverRuntimeInstancesParams are the parameters for the
// discover_runtime_instances operation. There are none — it always scans
// the configured PortRange — but every tool has a typed params struct
// (even an empty one) for schema consistency, matching
// headless.ReadProjectSettingsParams' own precedent.
type DiscoverRuntimeInstancesParams struct{}

// DiscoverRuntimeInstances scans PortRange for live autoload listeners.
func (d *Discoverer) DiscoverRuntimeInstances(ctx context.Context, _ DiscoverRuntimeInstancesParams) ([]Instance, error) {
	return DiscoverInstances(ctx, d.PortRange, d.Logger)
}

// ReadRuntimeSceneTreeParams are the parameters for the
// read_runtime_scene_tree operation.
type ReadRuntimeSceneTreeParams struct {
	// Port is a live instance's port, from a DiscoverRuntimeInstances
	// result.
	Port int `json:"port"`
}

// ReadRuntimeSceneTree reads the live scene tree of the instance listening
// on params.Port (found via DiscoverRuntimeInstances).
func (d *Discoverer) ReadRuntimeSceneTree(ctx context.Context, params ReadRuntimeSceneTreeParams) (*RuntimeSceneNode, error) {
	c := &Client{Port: params.Port, Logger: d.Logger}
	return c.ReadRuntimeSceneTree(ctx)
}

// ReadRuntimeNodePropertyToolParams are the parameters for the
// read_runtime_node_property operation.
type ReadRuntimeNodePropertyToolParams struct {
	// Port is a live instance's port, from a DiscoverRuntimeInstances
	// result.
	Port int `json:"port"`
	// NodePath addresses the target node relative to the current scene
	// root, using Godot's own NodePath syntax. Empty string means the
	// scene root itself.
	NodePath string `json:"node_path"`
	// PropertyName is the live node property to read.
	PropertyName string `json:"property_name"`
}

// ReadRuntimeNodeProperty reads a single live property value from the
// instance listening on params.Port (found via DiscoverRuntimeInstances).
func (d *Discoverer) ReadRuntimeNodeProperty(ctx context.Context, params ReadRuntimeNodePropertyToolParams) (*RuntimeNodePropertyResult, error) {
	c := &Client{Port: params.Port, Logger: d.Logger}
	return c.ReadRuntimeNodeProperty(ctx, params.NodePath, params.PropertyName)
}

// call sends a single fixed operation name (never free-form/user-supplied
// text interpreted as code) with an optional structured params payload,
// and decodes the structured JSON response.
func (c *Client) call(ctx context.Context, operation string, params, out any) error {
	conn, err := c.Dialer.DialContext(ctx, "tcp", c.addr())
	if err != nil {
		return fmt.Errorf("runtime: dialing %s: %w", c.addr(), err)
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	if err := json.NewEncoder(conn).Encode(request{Operation: operation, Params: params}); err != nil {
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
