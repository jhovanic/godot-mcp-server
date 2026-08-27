package validate

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRoot_Resolve(t *testing.T) {
	root := t.TempDir()

	// A real, resolvable subdirectory and file inside the root, so the
	// "valid in-root path" case has something to resolve against.
	if err := os.MkdirAll(filepath.Join(root, "scenes"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "scenes", "main.tscn"), []byte("[gd_scene]"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// A sibling directory outside the root, and a symlink inside the root
	// that points at it — the symlink-escape case.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	symlinkSupported := true
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		if runtime.GOOS == "windows" {
			symlinkSupported = false // symlinks often require elevated perms on Windows CI
		} else {
			t.Fatalf("setup: %v", err)
		}
	}

	r, err := NewRoot(root)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}

	tests := []struct {
		name        string
		path        string
		wantErr     bool
		wantOutside bool // whether the error should wrap ErrOutsideRoot specifically
		skip        bool
	}{
		{
			name:    "valid in-root path",
			path:    "scenes/main.tscn",
			wantErr: false,
		},
		{
			name:        "traversal attempt with ../",
			path:        "../outside.txt",
			wantErr:     true,
			wantOutside: true,
		},
		{
			name:        "traversal attempt buried mid-path",
			path:        "scenes/../../outside.txt",
			wantErr:     true,
			wantOutside: true,
		},
		{
			name:        "absolute path outside the project root",
			path:        filepath.Join(outside, "secret.txt"),
			wantErr:     true,
			wantOutside: true,
		},
		{
			name:        "absolute path that happens to point inside the root",
			path:        filepath.Join(root, "scenes", "main.tscn"),
			wantErr:     true, // absolute input is rejected outright, regardless of destination
			wantOutside: true,
		},
		{
			name:    "empty path",
			path:    "",
			wantErr: true,
			// empty-path is a distinct validation error, not an
			// ErrOutsideRoot case.
		},
		{
			name:        "symlink inside root pointing outside it",
			path:        "escape/secret.txt",
			wantErr:     true,
			wantOutside: true,
			skip:        !symlinkSupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skip {
				t.Skip("symlinks not supported in this environment")
			}
			got, err := r.Resolve(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%q) = %q, want error", tt.path, got)
				}
				if tt.wantOutside && !errors.Is(err, ErrOutsideRoot) {
					t.Fatalf("Resolve(%q) error = %v, want wrapping ErrOutsideRoot", tt.path, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q) unexpected error: %v", tt.path, err)
			}
			if !r.contains(got) {
				t.Fatalf("Resolve(%q) = %q, not inside root %q", tt.path, got, r.abs)
			}
		})
	}
}

func TestNewRoot_RejectsNonDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := NewRoot(file); err == nil {
		t.Fatal("NewRoot on a file, want error")
	}
}

func TestNewRoot_RejectsEmpty(t *testing.T) {
	if _, err := NewRoot(""); err == nil {
		t.Fatal("NewRoot(\"\"), want error")
	}
}
