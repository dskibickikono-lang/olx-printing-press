// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dskibickikono-lang/olx-pp-cli/internal/bizraport"
	"github.com/dskibickikono-lang/olx-pp-cli/internal/config"
	"github.com/spf13/cobra"
)

type doctorFlags struct {
	offline bool
	live    bool
}

func newDoctorCmd(root *rootFlags) *cobra.Command {
	f := &doctorFlags{}
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check OLX connectivity, store health, and config paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd.Context(), cmd, root, f)
		},
	}
	cmd.Flags().BoolVar(&f.offline, "offline", true, "Check local state only (default)")
	cmd.Flags().BoolVar(&f.live, "live", false, "Make a real HTTP request to OLX to check connectivity")
	cmd.MarkFlagsMutuallyExclusive("offline", "live")
	return cmd
}

func runDoctor(ctx context.Context, cmd *cobra.Command, root *rootFlags, f *doctorFlags) error {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "olx-pp-cli %s\n", version)
	fmt.Fprintf(out, "project root : %s\n", projectRoot())
	fmt.Fprintf(out, "db path      : %s\n", resolveDBPath(root.dbPath))
	fmt.Fprintf(out, "cache dir    : %s\n", resolveCacheDir(root.cacheDir))
	fmt.Fprintf(out, "export dir   : %s\n", resolveExportDir(root.exportDir))
	fmt.Fprintf(out, "rps          : www=%.2f jobs=%.2f phones=%.2f\n", root.rpsWWW, root.rpsJobs, root.rpsPhones)

	if f.live {
		fmt.Fprintln(out, "WARNING: this will make a live OLX request")
		// In live mode, we open read-write to ensure migrations and schema.
		st, err := openStore(ctx, root)
		if err != nil {
			fmt.Fprintf(out, "store        : FAIL — %v\n", err)
			return err
		}
		defer st.Close()
		fmt.Fprintln(out, "store        : OK")

		// Quick live ping: try a 1-page listing on the smallest category.
		pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		client := newOLXClient(root)
		listings, _, err := client.ListByCategory(pingCtx, defaultCategoryIDs[0], 0, 5)
		if err != nil {
			fmt.Fprintf(out, "olx ping     : FAIL — %v\n", err)
			return nil // doctor is informational; don't exit non-zero on network warnings
		}
		fmt.Fprintf(out, "olx ping     : OK — %d listings in 5-row probe of category=%d\n", len(listings), defaultCategoryIDs[0])
	} else {
		// Offline mode: just check DB. A missing file is informational, not
		// a failure — it just means nothing has been synced yet.
		dbPath := resolveDBPath(root.dbPath)
		if _, statErr := os.Stat(dbPath); os.IsNotExist(statErr) {
			fmt.Fprintln(out, "store        : MISSING — run `olx-pp-cli sync`")
		} else {
			st, err := openStoreReadOnly(ctx, root)
			if err != nil {
				fmt.Fprintf(out, "store        : FAIL — %v\n", err)
				return err
			}
			defer st.Close()
			fmt.Fprintln(out, "store        : OK (offline)")
		}
	}

	reportBizraport(ctx, out, f.live)
	return nil
}

// reportBizraport prints the bizraport.pl credential status (masked) and,
// in live mode, the month-to-date usage/cost.
func reportBizraport(ctx context.Context, out io.Writer, live bool) {
	cfg, err := config.Load("")
	if err != nil || !cfg.Bizraport.Configured() {
		fmt.Fprintln(out, "bizraport    : not configured (set [bizraport] email/password or BIZRAPORT_EMAIL/BIZRAPORT_PASSWORD)")
		return
	}
	fmt.Fprintf(out, "bizraport    : configured (email=%s, password=set)\n", maskEmail(cfg.Bizraport.Email))
	if !live {
		return
	}
	client := bizraport.New(bizraport.Options{
		Email:    cfg.Bizraport.Email,
		Password: cfg.Bizraport.Password,
		BaseURL:  cfg.Bizraport.BaseURL,
		PerSec:   cfg.Bizraport.RPS,
	})
	usageCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	u, err := client.Usage(usageCtx)
	if err != nil {
		fmt.Fprintf(out, "bizraport use: FAIL — %v\n", err)
		return
	}
	fmt.Fprintf(out, "bizraport use: %s — %.2f PLN net month-to-date\n", u.Miesiac, u.KosztNettoPLN)
}

// maskEmail keeps the first character and domain, hiding the rest, so a
// configured address is recognizable in doctor output without leaking it.
func maskEmail(email string) string {
	at := strings.IndexByte(email, '@')
	if at <= 0 {
		return "***"
	}
	first := email[:1]
	return first + "***" + email[at:]
}
