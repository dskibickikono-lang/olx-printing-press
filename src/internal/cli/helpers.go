// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/dskibickikono-lang/olx-pp-cli/internal/olx"
	"github.com/dskibickikono-lang/olx-pp-cli/internal/store"
)

// usageError marks an error caused by bad command-line usage. ExitCode
// maps these to exit status 2.
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }
func usageErr(format string, a ...any) error {
	return &usageError{fmt.Errorf(format, a...)}
}

// projectRoot walks up from the binary's location (or CWD when binary
// path is unavailable) looking for a directory that has both `bin/` and
// `data/` subdirectories — the marker for the OLX project root.
// Returns "." if none is found.
func projectRoot() string {
	if exe, err := os.Executable(); err == nil {
		if root := walkUp(filepath.Dir(exe)); root != "" {
			return root
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		if root := walkUp(cwd); root != "" {
			return root
		}
	}
	return "."
}

func walkUp(dir string) string {
	for i := 0; i < 6 && dir != "" && dir != "/"; i++ {
		bin, errB := os.Stat(filepath.Join(dir, "bin"))
		data, errD := os.Stat(filepath.Join(dir, "data"))
		if errB == nil && bin.IsDir() && errD == nil && data.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func resolveDBPath(flag string) string {
	if flag != "" {
		return flag
	}
	if env := os.Getenv("OLX_PP_DB"); env != "" {
		return env
	}
	return filepath.Join(projectRoot(), "data", "olx_jobs.db")
}

func resolveCacheDir(flag string) string {
	if flag != "" {
		return flag
	}
	if env := os.Getenv("OLX_PP_CACHE"); env != "" {
		return env
	}
	return filepath.Join(projectRoot(), "data", "cache")
}

func resolveExportDir(flag string) string {
	if flag != "" {
		return flag
	}
	if env := os.Getenv("OLX_PP_EXPORTS"); env != "" {
		return env
	}
	return filepath.Join(projectRoot(), "data", "exports")
}

// openStore opens the SQLite store, creating directories as needed and
// ensuring the OLX schema is present.
func openStore(ctx context.Context, flags *rootFlags) (*store.Store, error) {
	path := resolveDBPath(flags.dbPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("ensure db dir: %w", err)
	}
	s, err := store.OpenWithContext(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("open store at %s: %w", path, err)
	}
	if err := s.EnsureOLXSchema(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("ensure OLX schema: %w", err)
	}
	return s, nil
}

func openStoreReadOnly(ctx context.Context, flags *rootFlags) (*store.Store, error) {
	path := resolveDBPath(flags.dbPath)
	s, err := store.OpenReadOnly(path)
	if err != nil {
		return nil, fmt.Errorf("open read-only store at %s: %w", path, err)
	}
	return s, nil
}

func newOLXClient(flags *rootFlags) *olx.Client {
	return olx.New(olx.Options{
		WWWPerSec:    flags.rpsWWW,
		JobsPerSec:   flags.rpsJobs,
		PhonesPerSec: flags.rpsPhones,
	})
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func printTable(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	if len(headers) > 0 {
		for i, h := range headers {
			if i > 0 {
				fmt.Fprint(tw, "\t")
			}
			fmt.Fprint(tw, h)
		}
		fmt.Fprintln(tw)
	}
	for _, row := range rows {
		for i, c := range row {
			if i > 0 {
				fmt.Fprint(tw, "\t")
			}
			fmt.Fprint(tw, c)
		}
		fmt.Fprintln(tw)
	}
	return tw.Flush()
}

// parseDuration tolerates plain duration strings plus a trailing "d" for
// days, which time.ParseDuration doesn't accept.
func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if n := len(s); n > 1 && (s[n-1] == 'd' || s[n-1] == 'D') {
		var days int
		if _, err := fmt.Sscanf(s[:n-1], "%d", &days); err == nil {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	return time.ParseDuration(s)
}
