package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"time"

	"github.com/attisvalentin/supacraft/internal/handler"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/spf13/cobra"
)

// tailBatch is how many changes one read takes; small enough that a page
// of large rows stays well under the remote client's response limit.
const tailBatch = 100

var (
	realtimeTenant string
	realtimeLocal  bool
	realtimeSince  int64
)

var realtimeCmd = &cobra.Command{
	Use:   "realtime",
	Short: "Follow the changes realtime streams",
	Long: `Shows the change log behind /realtime: the inserted, updated and deleted
rows of the api_settings.realtime tables (readable columns). Acts on the
central server when --server, --data-dir, SUPACRAFT_SERVER or
SUPACRAFT_DATA_DIR is set, otherwise (or with --local) on the project's own
database.

  supacraft realtime tail messages
  supacraft realtime tail --since 120`,
}

var realtimeTailCmd = &cobra.Command{
	Use:   "tail [table]",
	Short: "Print changes as they happen, one JSON object per line",
	Long: `Prints each change of the streamed tables (or only table) as one JSON line:
{seq, table, op, row, created_at}. Starts from now, or after --since <seq>
(changes are kept for 15 minutes). Stop with Ctrl-C.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		t, id, err := projectTarget(realtimeTenant, realtimeLocal)
		if err != nil {
			return err
		}
		table := ""
		if len(args) == 1 {
			table = args[0]
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		read := func(since int64, limit int) ([]tenant.Change, int64, error) {
			if t.client == nil {
				return tenant.ListTenantChanges(ctx, t.dataDir, id, table, since, limit)
			}
			var out handler.ChangesResponse
			q := url.Values{"since": {fmt.Sprint(since)}, "limit": {fmt.Sprint(limit)}}
			if table != "" {
				q.Set("table", table)
			}
			err := t.client.Do(ctx, http.MethodGet, "/admin/tenants/"+url.PathEscape(id)+"/changes?"+q.Encode(), nil, &out)
			return out.Data, out.LastSeq, err
		}
		since := realtimeSince
		if !cmd.Flags().Changed("since") {
			if _, since, err = read(0, 0); err != nil {
				return tenantErr(id, err)
			}
		}
		fmt.Fprintf(os.Stderr, "Following changes of %s on %s after seq %d (Ctrl-C to stop)\n", id, t.where, since)
		enc := json.NewEncoder(os.Stdout)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			list, last, err := read(since, tailBatch)
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				return tenantErr(id, err)
			}
			for _, c := range list {
				if err := enc.Encode(c); err != nil {
					return err
				}
				since = c.Seq
			}
			if len(list) == tailBatch {
				continue
			}
			// Everything up to last was read; with a table filter the
			// other tables' changes needn't be scanned again.
			since = max(since, last)
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
			}
		}
	},
}

func init() {
	realtimeCmd.PersistentFlags().StringVar(&tenantsDataDir, "data-dir", "", "on the server: the central data directory (env SUPACRAFT_DATA_DIR)")
	realtimeCmd.PersistentFlags().StringVar(&tenantsServer, "server", "", "URL of the central server (env SUPACRAFT_SERVER)")
	realtimeCmd.PersistentFlags().StringVar(&realtimeTenant, "tenant", "", "tenant id (default: the project in the current directory)")
	realtimeCmd.PersistentFlags().BoolVar(&realtimeLocal, "local", false, "use the project's own database in .supacraft even if a server is configured")
	realtimeTailCmd.Flags().Int64Var(&realtimeSince, "since", 0, "start after this seq instead of now")
	realtimeCmd.AddCommand(realtimeTailCmd)
	rootCmd.AddCommand(realtimeCmd)
}
