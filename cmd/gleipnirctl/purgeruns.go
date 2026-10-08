package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"
)

func newPurgeRunsCmd() *cobra.Command {
	var dbPath, olderThan string
	var statuses []string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "purge-runs --older-than <duration>",
		Short: "Delete old terminal runs and their steps directly from the database",
		Long: `Deletes runs that finished more than --older-than ago, together with their
steps, approval/feedback/tool-input requests, and other run-scoped rows.

--older-than is required and takes a Go duration (36h) or whole days (90d).
A run's age is measured from completed_at, falling back to started_at when no
completion time was recorded.

Only terminal runs are ever deleted: by default complete, failed and
interrupted. --status narrows that set; naming a running, pending or
waiting_for_* status is an error.

Plugin audit events are security records and are kept; those tied to a purged
run lose their run link but are not deleted.

The whole purge is one write transaction. The server does not need to be
stopped; the command waits up to 10s for the write lock. Use --dry-run to see
the counts without deleting anything.`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			code := PurgeRuns(context.Background(), dbPath, PurgeOptions{
				OlderThan: olderThan,
				Statuses:  statuses,
				DryRun:    dryRun,
			}, cmd.OutOrStdout(), cmd.ErrOrStderr())
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}

	envDBPath := os.Getenv("GLEIPNIR_DB_PATH")
	if envDBPath == "" {
		envDBPath = defaultDBPath
	}

	cmd.Flags().StringVar(&olderThan, "older-than", "", "age threshold, a Go duration or whole days such as 90d (required)")
	cmd.Flags().StringSliceVar(&statuses, "status", defaultPurgeStatuses, "comma-separated terminal statuses to purge")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print counts without deleting anything")
	cmd.Flags().StringVar(&dbPath, "db-path", envDBPath, "path to the SQLite database file")

	return cmd
}
