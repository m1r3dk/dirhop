package cli

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/m1r3dk/dirhop/internal/app"
	"github.com/m1r3dk/dirhop/internal/output"
)

// newDB builds the `db` maintenance command group: inspecting size, finding
// duplicate sessions that index the same bucket, reclaiming soft-deleted rows,
// and shrinking the file on disk. Mutating actions are dry-run by default and
// only change data when --apply is passed.
func newDB(a *app.App, opt *options, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Inspect and clean up the local index database",
		Long: `Maintenance for dirhop's local SQLite index.

The index accumulates two kinds of bloat over time: duplicate sessions that
point at the same underlying bucket through different URL spellings, and
soft-deleted rows left behind by rescans. These never shrink the file on
their own. Use these subcommands to see where space is going and reclaim it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newDBStatus(a, opt, out), newDBDuplicates(a, opt, out), newDBPrune(a, opt, out), newDBVacuum(a, opt, out), newDBClean(a, opt, out))
	return cmd
}

func newDBStatus(a *app.App, opt *options, out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show per-session entry counts and soft-deleted rows",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			stats, err := a.DB.SiteEntryStats(cmd.Context())
			if err != nil {
				return err
			}
			if opt.json {
				return output.JSON(out, stats)
			}
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			defer w.Flush()
			fmt.Fprintln(w, "SESSION\tLIVE\tREMOVED\tSTATUS\tURL")
			var totalRemoved int64
			for _, s := range stats {
				totalRemoved += s.RemovedRows
				fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\n", s.Site.Name, s.LiveEntries, s.RemovedRows, s.Site.ScanStatus, s.Site.CanonicalURL)
			}
			if totalRemoved > 0 {
				fmt.Fprintf(w, "\t\t\t\t\n")
				fmt.Fprintf(w, "%d soft-deleted rows reclaimable with `dirhop db prune --apply`\n", totalRemoved)
			}
			return nil
		},
	}
}

func newDBDuplicates(a *app.App, opt *options, out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "duplicates",
		Short: "List sessions that index the same bucket under different URLs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			groups, err := a.Sessions.Duplicates(cmd.Context())
			if err != nil {
				return err
			}
			if opt.json {
				return output.JSON(out, groups)
			}
			if len(groups) == 0 {
				fmt.Fprintln(out, "No duplicate sessions found.")
				return nil
			}
			for _, g := range groups {
				fmt.Fprintf(out, "Bucket %s\n", g.Identity)
				fmt.Fprintf(out, "  keep      %s (%s)\n", g.Keep.Name, g.Keep.CanonicalURL)
				for _, d := range g.Duplicates {
					fmt.Fprintf(out, "  duplicate %s (%s)\n", d.Name, d.CanonicalURL)
				}
			}
			fmt.Fprintln(out, "\nRemove the duplicates with `dirhop db clean --apply`.")
			return nil
		},
	}
}

func newDBPrune(a *app.App, opt *options, out io.Writer) *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Hard-delete soft-removed rows (run db vacuum after to shrink the file)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			stats, err := a.DB.SiteEntryStats(cmd.Context())
			if err != nil {
				return err
			}
			var reclaimable int64
			for _, s := range stats {
				reclaimable += s.RemovedRows
			}
			if !apply {
				fmt.Fprintf(out, "Would delete %d soft-removed rows. Re-run with --apply.\n", reclaimable)
				return nil
			}
			deleted, err := a.DB.PurgeRemovedEntries(cmd.Context(), 0)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Deleted %d soft-removed rows. Run `dirhop db vacuum` to shrink the file.\n", deleted)
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "actually delete (default is a dry run)")
	return cmd
}

func newDBVacuum(a *app.App, _ *options, out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "vacuum",
		Short: "Checkpoint the WAL and rewrite the database to reclaim disk space",
		Long: `Fold the write-ahead log back into the database and VACUUM it.

VACUUM rewrites the whole file, so it can take a while on a large database and
temporarily needs free disk space roughly equal to the database size.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(out, "Checkpointing WAL...")
			if err := a.DB.CheckpointWAL(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(out, "Vacuuming (this may take a while on a large database)...")
			if err := a.DB.Vacuum(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(out, "Done.")
			return nil
		},
	}
}

func newDBClean(a *app.App, opt *options, out io.Writer) *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Remove duplicate sessions, prune soft-removed rows, and vacuum",
		Long: `End-to-end cleanup: delete duplicate sessions that index the same bucket,
hard-delete soft-removed rows, then checkpoint and VACUUM to shrink the file.

Dry-run by default: it prints exactly what it would remove. Pass --apply to
make the changes. The richest index for each bucket is always kept.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDBClean(cmd.Context(), a, opt, out, apply)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "actually delete and vacuum (default is a dry run)")
	return cmd
}

func runDBClean(ctx context.Context, a *app.App, opt *options, out io.Writer, apply bool) error {
	groups, err := a.Sessions.Duplicates(ctx)
	if err != nil {
		return err
	}
	stats, err := a.DB.SiteEntryStats(ctx)
	if err != nil {
		return err
	}
	var removableRows int64
	for _, s := range stats {
		removableRows += s.RemovedRows
	}
	dupCount := 0
	for _, g := range groups {
		dupCount += len(g.Duplicates)
	}

	if !apply {
		if dupCount == 0 && removableRows == 0 {
			fmt.Fprintln(out, "Nothing to clean: no duplicate sessions and no soft-removed rows.")
			return nil
		}
		for _, g := range groups {
			fmt.Fprintf(out, "Bucket %s: keep %s, remove %d duplicate session(s)\n", g.Identity, g.Keep.Name, len(g.Duplicates))
			for _, d := range g.Duplicates {
				fmt.Fprintf(out, "  - %s (%s)\n", d.Name, d.CanonicalURL)
			}
		}
		fmt.Fprintf(out, "Would delete %d duplicate session(s) and %d soft-removed row(s), then vacuum.\n", dupCount, removableRows)
		fmt.Fprintln(out, "Re-run with --apply to make these changes.")
		return nil
	}

	// Apply: delete duplicate sessions (cascades their entries), then purge
	// soft-removed rows, then reclaim space.
	removedSessions := 0
	for _, g := range groups {
		for _, d := range g.Duplicates {
			if err := a.Sessions.Remove(ctx, fmt.Sprintf("%d", d.ID)); err != nil {
				return fmt.Errorf("remove duplicate session %s: %w", d.Name, err)
			}
			removedSessions++
			fmt.Fprintf(out, "Removed duplicate session %s\n", d.Name)
		}
	}
	deleted, err := a.DB.PurgeRemovedEntries(ctx, 0)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Removed %d duplicate session(s); deleted %d soft-removed row(s).\n", removedSessions, deleted)
	fmt.Fprintln(out, "Checkpointing WAL and vacuuming...")
	if err := a.DB.CheckpointWAL(ctx); err != nil {
		return err
	}
	if err := a.DB.Vacuum(ctx); err != nil {
		return err
	}
	fmt.Fprintln(out, "Done.")
	return nil
}
