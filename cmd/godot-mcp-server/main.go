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
}

func parseFlags(args []string) (config, error) {
	fs := flag.NewFlagSet("godot-mcp-server", flag.ContinueOnError)

	var cfg config
	fs.StringVar(&cfg.projectRoot, "project", "", "path to the Godot project root (required); every file operation is scoped to this directory")
	fs.StringVar(&cfg.godotBin, "godot-bin", "godot", "path to (or name on PATH of) the Godot executable used for the headless CLI tier")
	fs.StringVar(&cfg.operationsScript, "operations-script", "", "path to the fixed headless operations script (default: scripts/godot_operations.gd next to this binary)")
	fs.StringVar(&cfg.auditLogPath, "audit-log", "", "optional path to also write the audit log to (audit entries are always written to stderr)")

	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if cfg.projectRoot == "" {
		return config{}, errors.New("-project is required")
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

func run(ctx context.Context, cfg config, stderr io.Writer) error {
	logWriter := stderr
	if cfg.auditLogPath != "" {
		f, err := os.OpenFile(cfg.auditLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("opening audit log %q: %w", cfg.auditLogPath, err)
		}
		defer func() { _ = f.Close() }()
		logWriter = io.MultiWriter(stderr, f)
	}
	logger := audit.New(logWriter)

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
	tools.RegisterAll(server, tools.Deps{Headless: headlessClient, Logger: logger})

	_, _ = fmt.Fprintf(stderr, "godot-mcp-server: project root %s, serving over stdio\n", root.String())
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
