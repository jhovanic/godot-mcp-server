// Package validate provides path-validation helpers that confine every file
// operation to a single configured project root.
//
// This package is security-critical: it is the only thing standing between a
// tool parameter supplied by an untrusted AI client and the filesystem. Per
// SECURITY.md, path traversal is rejected outright, not sanitized-and-allowed
// — so this package never attempts to "fix up" a suspicious path, it refuses
// it.
package validate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrOutsideRoot is returned (wrapped) when a candidate path would resolve
// outside the configured project root.
var ErrOutsideRoot = errors.New("path resolves outside the project root")

// Root represents a validated project root that every file operation is
// confined to. It is resolved once at construction time (symlinks included)
// so that later checks compare against a canonical, non-spoofable base path.
type Root struct {
	// abs is the canonical, symlink-resolved absolute path of the project
	// root, guaranteed to end without a trailing separator (except for the
	// filesystem root itself).
	abs string
}

// NewRoot validates that path exists, is a directory, and resolves it to a
// canonical absolute path (symlinks included). The returned Root is the
// trust boundary for every subsequent call to Resolve.
func NewRoot(path string) (*Root, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("validate: project root must not be empty")
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("validate: resolving project root: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("validate: resolving project root symlinks: %w", err)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("validate: stat project root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("validate: project root %q is not a directory", resolved)
	}

	return &Root{abs: filepath.Clean(resolved)}, nil
}

// String returns the canonical absolute project root path.
func (r *Root) String() string {
	return r.abs
}

// Resolve validates that rel, interpreted as a path relative to the project
// root, resolves to a location inside the project root, and returns its
// absolute path.
//
// Resolve is intentionally strict:
//   - rel must be a relative path. Absolute paths are rejected outright,
//     even if they happen to point inside the root — tool parameters are
//     always project-relative by contract, and accepting absolute input
//     would let a caller bypass the root by construction.
//   - Any ".." path element in rel is rejected outright. Traversal attempts
//     are refused, not cleaned away and silently re-checked.
//   - After joining and cleaning, the result is re-verified to still be
//     inside the root (defense in depth against any of the above being
//     bypassed by a future refactor).
//   - If the resolved path already exists on disk, its symlinks are
//     resolved and the *real* target is re-checked against the root, so a
//     symlink planted inside the project that points outside it cannot be
//     used to escape the sandbox.
func (r *Root) Resolve(rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", errors.New("validate: path must not be empty")
	}

	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("validate: %w: %q is an absolute path, expected project-relative", ErrOutsideRoot, rel)
	}

	// Reject traversal outright, before any cleaning, on both native and
	// forward-slash separators (tool params are typically forward-slash,
	// Godot res://-style paths).
	for _, part := range strings.FieldsFunc(rel, func(c rune) bool { return c == '/' || c == '\\' }) {
		if part == ".." {
			return "", fmt.Errorf("validate: %w: %q contains a \"..\" traversal segment", ErrOutsideRoot, rel)
		}
	}

	joined := filepath.Join(r.abs, filepath.FromSlash(rel))
	cleaned := filepath.Clean(joined)

	if !r.contains(cleaned) {
		return "", fmt.Errorf("validate: %w: %q", ErrOutsideRoot, rel)
	}

	// Defense in depth: if the target exists, resolve symlinks and check
	// the real path too, so an in-root symlink can't point outside it.
	if real, err := filepath.EvalSymlinks(cleaned); err == nil {
		if !r.contains(real) {
			return "", fmt.Errorf("validate: %w: %q resolves through a symlink to a location outside the root", ErrOutsideRoot, rel)
		}
	}
	// If the path doesn't exist yet (err != nil), there's nothing to
	// resolve — the cleaned/contains check above already covers it, and
	// write operations that create new files should re-validate once the
	// symlink-free parent is known to exist.

	return cleaned, nil
}

// contains reports whether abs is r's root itself, or a path underneath it.
func (r *Root) contains(abs string) bool {
	if abs == r.abs {
		return true
	}
	return strings.HasPrefix(abs, r.abs+string(filepath.Separator))
}
