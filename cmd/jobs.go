package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/attisvalentin/supacraft/internal/handler"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/spf13/cobra"
)

var (
	jobsTenant string
	jobsLocal  bool
	jobsStatus string
	jobsLimit  int
)

var jobsCmd = &cobra.Command{
	Use:   "jobs",
	Short: "List background jobs and retry failed ones",
	Long: `Shows the background jobs queued by job.enqueue steps and retries failed
ones. Acts on the central server when --server, --data-dir, SUPACRAFT_SERVER
or SUPACRAFT_DATA_DIR is set, otherwise (or with --local) on the project's
own database.

  supacraft jobs list --status failed
  supacraft jobs retry <job-id>`,
}

var jobsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List jobs, newest first",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		t, id, err := projectTarget(jobsTenant, jobsLocal)
		if err != nil {
			return err
		}
		var list []tenant.Job
		if t.client != nil {
			var out struct {
				Data []tenant.Job `json:"data"`
			}
			q := url.Values{"limit": {fmt.Sprint(jobsLimit)}}
			if jobsStatus != "" {
				q.Set("status", jobsStatus)
			}
			err = t.client.Do(cmd.Context(), http.MethodGet, "/admin/tenants/"+url.PathEscape(id)+"/jobs?"+q.Encode(), nil, &out)
			list = out.Data
		} else {
			list, err = tenant.ListTenantJobs(cmd.Context(), t.dataDir, id, jobsStatus, min(max(jobsLimit, 1), 500))
		}
		if err != nil {
			return tenantErr(id, err)
		}
		if len(list) == 0 {
			fmt.Printf("No jobs in %s on %s\n", id, t.where)
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tFUNCTION\tSTATUS\tATTEMPTS\tRUN AT\tERROR")
		for _, j := range list {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t%s\t%s\n", j.ID, j.Function, j.Status, j.Attempts, j.MaxAttempts, j.RunAt.Format("2006-01-02 15:04:05"), oneLine(j.LastError))
		}
		return w.Flush()
	},
}

var jobsRetryCmd = &cobra.Command{
	Use:   "retry <job-id>",
	Short: "Queue a failed job again",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		t, id, err := projectTarget(jobsTenant, jobsLocal)
		if err != nil {
			return err
		}
		if t.client != nil {
			err = t.client.Do(cmd.Context(), http.MethodPost, "/admin/tenants/"+url.PathEscape(id)+"/jobs/"+url.PathEscape(args[0])+"/retry", nil, nil)
		} else {
			err = tenant.RetryTenantJob(cmd.Context(), t.dataDir, id, args[0])
		}
		if err != nil {
			return tenantErr(id, err)
		}
		fmt.Printf("✓ Queued %s again; a running server picks it up within a minute\n", args[0])
		return nil
	},
}

var (
	jobsArgs  string
	jobsDelay int
)

var jobsEnqueueCmd = &cobra.Command{
	Use:   "enqueue <function>",
	Short: "Queue a PRIVATE function to run as a job",
	Long: `Queues a PRIVATE function to run in the background, as a job.enqueue step
would. --args is a JSON object the function gets as args.*.

  supacraft jobs enqueue send_invoice --args '{"order_id": "..."}'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		jobArgs := map[string]any{}
		if jobsArgs != "" {
			if err := json.Unmarshal([]byte(jobsArgs), &jobArgs); err != nil {
				return fmt.Errorf("--args must be a JSON object: %w", err)
			}
		}
		t, id, err := projectTarget(jobsTenant, jobsLocal)
		if err != nil {
			return err
		}
		var jobID string
		if t.client != nil {
			var out struct {
				Data struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			err = t.client.Do(cmd.Context(), http.MethodPost, "/admin/tenants/"+url.PathEscape(id)+"/jobs", handler.EnqueueRequest{Function: args[0], Args: jobArgs, Delay: jobsDelay}, &out)
			jobID = out.Data.ID
		} else {
			jobID, err = tenant.EnqueueTenantJob(cmd.Context(), t.dataDir, id, args[0], jobArgs, jobsDelay)
		}
		if err != nil {
			return tenantErr(id, err)
		}
		fmt.Printf("✓ Queued %s as job %s on %s\n", args[0], jobID, t.where)
		return nil
	},
}

func init() {
	jobsEnqueueCmd.Flags().StringVar(&jobsArgs, "args", "", "JSON object passed to the function as args.*")
	jobsEnqueueCmd.Flags().IntVar(&jobsDelay, "delay", 0, "seconds to wait before it runs")
	jobsCmd.PersistentFlags().StringVar(&tenantsDataDir, "data-dir", "", "on the server: the central data directory (env SUPACRAFT_DATA_DIR)")
	jobsCmd.PersistentFlags().StringVar(&tenantsServer, "server", "", "URL of the central server (env SUPACRAFT_SERVER)")
	jobsCmd.PersistentFlags().StringVar(&jobsTenant, "tenant", "", "tenant id (default: the project in the current directory)")
	jobsCmd.PersistentFlags().BoolVar(&jobsLocal, "local", false, "use the project's own database in .supacraft even if a server is configured")
	jobsListCmd.Flags().StringVar(&jobsStatus, "status", "", "only jobs with this status: pending, running, done or failed")
	jobsListCmd.Flags().IntVar(&jobsLimit, "limit", 50, "how many jobs to show (max 500)")
	jobsCmd.AddCommand(jobsListCmd, jobsRetryCmd, jobsEnqueueCmd)
	rootCmd.AddCommand(jobsCmd)
}
