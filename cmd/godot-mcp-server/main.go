// Command godot-mcp-server is a self-hosted MCP server for Godot. It talks
// to an MCP client over stdio and exposes a small, explicit, auditable set
// of scoped operations against a single configured Godot project.
//
// See README.md for the two-tier architecture and SECURITY.md for the
// threat model this binary is built to.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jhovanic/godot-mcp-server/internal/audit"
	"github.com/jhovanic/godot-mcp-server/internal/headless"
	"github.com/jhovanic/godot-mcp-server/internal/tools"
	"github.com/jhovanic/godot-mcp-server/internal/validate"
)

// serverVersion is the MCP server's self-reported version. Overridden at
// build time via -ldflags "-X main.serverVersion=..." by .goreleaser.yaml;
// defaults to this placeholder for `go build`/`go run` during development.
var serverVersion = "0.0.0-dev"

// The TCP runtime tier (internal/runtime) isn't wired up here yet: no
// runtime-tier operation has been added to the tool allowlist, so there's
// nothing for a -runtime-* flag to configure. Add the flag(s) alongside the
// first tool that actually uses internal/runtime.Client, so the CLI surface
// only ever describes what's real.
type config struct {
	projectRoot      string
	godotBin         string
	operationsScript string
	auditLogPath     string
	mode             string
}

func parseFlags(args []string) (config, error) {
	fs := flag.NewFlagSet("godot-mcp-server", flag.ContinueOnError)

	var cfg config
	fs.StringVar(&cfg.projectRoot, "project", "", "path to the Godot project root (required); every file operation is scoped to this directory")
	fs.StringVar(&cfg.godotBin, "godot-bin", "godot", "path to (or name on PATH of) the Godot executable used for the headless CLI tier")
	fs.StringVar(&cfg.operationsScript, "operations-script", "", "path to the fixed headless operations script (default: scripts/godot_operations.gd next to this binary)")
	fs.StringVar(&cfg.auditLogPath, "audit-log", "", "optional additional path to write the audit log to (entries are always written to stderr and to logs/<session>.txt next to this binary)")
	fs.StringVar(&cfg.mode, "mode", string(tools.ModeReadOnly), fmt.Sprintf("which tools to expose: %q, %q, or %q (a strict superset of %q that additionally unlocks set_function_body and write_text_resource's script_path option — see SECURITY.md before using it); write tools are never advertised to the MCP client outside %q and %q", tools.ModeReadOnly, tools.ModeReadWrite, tools.ModeAdvanced, tools.ModeReadWrite, tools.ModeReadWrite, tools.ModeAdvanced))

	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if cfg.projectRoot == "" {
		return config{}, errors.New("-project is required")
	}
	if cfg.mode != string(tools.ModeReadOnly) && cfg.mode != string(tools.ModeReadWrite) && cfg.mode != string(tools.ModeAdvanced) {
		return config{}, fmt.Errorf("-mode %q: must be %q, %q, or %q", cfg.mode, tools.ModeReadOnly, tools.ModeReadWrite, tools.ModeAdvanced)
	}
	return cfg, nil
}

func defaultOperationsScriptPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating default operations script: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "scripts", "godot_operations.gd"), nil
}

// sessionID returns a filesystem-safe, human-sortable identifier for one
// run of this process: a UTC timestamp plus the PID (in case two runs
// start within the same second). Used to name this session's own audit log
// file so a human can find it later without any configuration.
func sessionID(now time.Time, pid int) string {
	return fmt.Sprintf("%s-%d", now.UTC().Format("20060102T150405Z"), pid)
}

// logFilePath returns where a session's audit log file should live:
// logs/<sessionID>.txt inside binDir. binDir is the directory containing
// the running binary in production (see openDefaultLogFile); taking it as
// a parameter here keeps this half of the logic unit-testable without
// touching the filesystem.
func logFilePath(binDir, sessionID string) string {
	return filepath.Join(binDir, "logs", sessionID+".txt")
}

// openDefaultLogFile opens (creating logs/ next to the binary if needed)
// this session's own audit log file, so a human has somewhere durable to
// look later beyond stderr — which is only as durable as whatever this
// process's MCP client host does with it (SECURITY.md requires audit
// logging independent of the client's own logs, and stderr alone doesn't
// give a human a file to come back to).
//
// This returns an error rather than being fatal: it's a convenience on top
// of the always-on stderr audit trail, not a substitute for it, so a
// filesystem problem here (e.g. a read-only install location) shouldn't
// stop the server from starting — the caller logs a warning and continues
// with stderr (and any explicit -audit-log path) alone.
func openDefaultLogFile(now time.Time, pid int) (f *os.File, path string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, "", fmt.Errorf("locating binary directory: %w", err)
	}
	path = logFilePath(filepath.Dir(exe), sessionID(now, pid))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, "", fmt.Errorf("creating log directory: %w", err)
	}
	f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, "", fmt.Errorf("opening %q: %w", path, err)
	}
	return f, path, nil
}

func run(ctx context.Context, cfg config, stderr io.Writer) error {
	logWriters := []io.Writer{stderr}

	if f, path, err := openDefaultLogFile(time.Now(), os.Getpid()); err != nil {
		_, _ = fmt.Fprintf(stderr, "godot-mcp-server: warning: could not open default log file: %v (audit log will only go to stderr and any -audit-log path)\n", err)
	} else {
		defer func() { _ = f.Close() }()
		logWriters = append(logWriters, f)
		_, _ = fmt.Fprintf(stderr, "godot-mcp-server: audit log also written to %s\n", path)
	}

	if cfg.auditLogPath != "" {
		f, err := os.OpenFile(cfg.auditLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("opening audit log %q: %w", cfg.auditLogPath, err)
		}
		defer func() { _ = f.Close() }()
		logWriters = append(logWriters, f)
	}
	logger := audit.New(io.MultiWriter(logWriters...))

	root, err := validate.NewRoot(cfg.projectRoot)
	if err != nil {
		return fmt.Errorf("project root: %w", err)
	}

	opsScript := cfg.operationsScript
	if opsScript == "" {
		opsScript, err = defaultOperationsScriptPath()
		if err != nil {
			return err
		}
	}
	if _, err := os.Stat(opsScript); err != nil {
		return fmt.Errorf("operations script %q: %w (pass -operations-script explicitly if it isn't installed next to the binary)", opsScript, err)
	}

	headlessClient := &headless.Client{
		GodotBin:         cfg.godotBin,
		OperationsScript: opsScript,
		Root:             root,
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "godot-mcp-server", Version: serverVersion}, nil)
	tools.RegisterAll(server, tools.Deps{
		SceneTree:         headlessClient,
		Script:            headlessClient,
		ProjectSettings:   headlessClient,
		TextResource:      headlessClient,
		BinaryResource:    headlessClient,
		ImportSettings:    headlessClient,
		NodeProperty:      headlessClient,
		AddNode:           headlessClient,
		RemoveNode:        headlessClient,
		ReparentNode:      headlessClient,
		ScriptExport:      headlessClient,
		ScriptSignal:      headlessClient,
		ScriptIdentity:    headlessClient,
		FunctionBody:      headlessClient,
		WriteTextResource: headlessClient,
		Mode:              tools.Mode(cfg.mode),
		Logger:            logger,
	})

	if cfg.mode == string(tools.ModeAdvanced) {
		_, _ = fmt.Fprintln(stderr, "godot-mcp-server: WARNING: -mode advanced is enabled — the connected AI client can write and replace executable GDScript logic in this project, and write_text_resource can instantiate and run any project script via script_path. See SECURITY.md before using this mode.")
	}
	_, _ = fmt.Fprintf(stderr, "godot-mcp-server: project root %s, mode %s, serving over stdio\n", root.String(), cfg.mode)
	return server.Run(ctx, &mcp.StdioTransport{})
}

func main() {
	// Reserve stdout exclusively for MCP JSON-RPC traffic (server.Run
	// writes protocol messages there over the stdio transport). Every
	// other log line — startup diagnostics and the standard logger's own
	// output — goes to stderr.
	log.SetOutput(os.Stderr)
	log.SetFlags(0)

	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		log.Fatalf("godot-mcp-server: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, cfg, os.Stderr); err != nil {
		log.Fatalf("godot-mcp-server: %v", err)
	}
}
