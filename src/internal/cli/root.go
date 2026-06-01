// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

// Package cli is the olx-pp-cli command tree built on top of the OLX
// client + the local SQLite store. Designed for sales-intelligence use:
// sync OLX job listings into a local database, surface companies posting
// many ads, export results for downstream tooling (Pipedrive etc).
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const version = "0.1.0"

// rootFlags holds the persistent flags shared across all commands.
type rootFlags struct {
	dbPath    string // --db
	cacheDir  string // --cache-dir
	exportDir string // --export-dir
	asJSON    bool   // --json
	timeout   time.Duration
	rpsWWW    float64 // --rps-www
	rpsJobs   float64 // --rps-jobs
	rpsPhones float64 // --rps-phones
}

// Execute runs the CLI.
func Execute() error {
	flags := &rootFlags{}
	root := newRootCmd(flags)
	return root.Execute()
}

// ExitCode maps an error to a Unix exit code.
//
//   - nil → 0
//   - usage errors (cobra/pflag) → 2
//   - everything else → 1
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ue *usageError
	if errors.As(err, &ue) {
		return 2
	}
	return 1
}

// RootCmd returns the Cobra command tree without executing it. Used by
// the MCP server to mirror every CLI command as an agent tool.
func RootCmd() *cobra.Command {
	return newRootCmd(&rootFlags{})
}

func newRootCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "olx-pp-cli",
		Short: "Sync OLX.pl job listings and detect high-volume employers",
		Long: `olx-pp-cli pulls public OLX.pl job listings (production / warehouse /
logistics by default) into a local SQLite database, then lets you surface
companies posting many ads and export the results.

Read-only. No OLX account or API key required.`,
		SilenceUsage:  true,
		SilenceErrors: false,
		Version:       version,
	}
	cmd.SetVersionTemplate("olx-pp-cli {{.Version}}\n")

	cmd.PersistentFlags().StringVar(&flags.dbPath, "db", "", "SQLite database path (default: $OLX_PP_DB or <project>/data/olx_jobs.db)")
	cmd.PersistentFlags().StringVar(&flags.cacheDir, "cache-dir", "", "HTTP cache directory (default: <project>/data/cache)")
	cmd.PersistentFlags().StringVar(&flags.exportDir, "export-dir", "", "Default directory for export outputs (default: <project>/data/exports)")
	cmd.PersistentFlags().BoolVar(&flags.asJSON, "json", false, "Output as JSON instead of a table")
	cmd.PersistentFlags().DurationVar(&flags.timeout, "timeout", 30*time.Second, "Per-request HTTP timeout")
	cmd.PersistentFlags().Float64Var(&flags.rpsWWW, "rps-www", 1.0, "Max requests/sec to www.olx.pl")
	cmd.PersistentFlags().Float64Var(&flags.rpsJobs, "rps-jobs", 0.5, "Max requests/sec to jobs-api.olx.pl")
	cmd.PersistentFlags().Float64Var(&flags.rpsPhones, "rps-phones", 0.2, "Max requests/sec to OLX limited-phones endpoint (it trips anti-abuse fast)")

	cmd.AddCommand(newSyncCmd(flags))
	cmd.AddCommand(newJobsCmd(flags))
	cmd.AddCommand(newCompaniesCmd(flags))
	cmd.AddCommand(newExportCmd(flags))
	cmd.AddCommand(newDoctorCmd(flags))

	return cmd
}

// withSignalContext returns a context that is canceled when the user
// hits Ctrl-C, plus a cleanup func to defer. All commands that do work
// should use this so SIGINT during a long sync interrupts cleanly.
func withSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-ch:
			fmt.Fprintln(os.Stderr, "interrupted, finishing in-flight work…")
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(ch)
	}()
	return ctx, cancel
}
