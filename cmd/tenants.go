package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/handler"
	"github.com/attisvalentin/supacraft/internal/remote"
	"github.com/attisvalentin/supacraft/internal/secrets"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/spf13/cobra"
)

var (
	tenantsDataDir string
	tenantsServer  string
	tenantsPurge   bool
	tenantsYes     bool
	tenantsLimit   int
)

var tenantsCmd = &cobra.Command{
	Use:   "tenants",
	Short: "List and manage the tenants of a central server",
	Long: `Manage the tenants served by "supacraft serve --data-dir", from your machine
with --server (signed with SUPACRAFT_ADMIN_SECRET), or on the server itself
with --data-dir. A running server picks every change up within seconds.

  supacraft tenants list --server https://api.example.com
  supacraft tenants errors shop --server https://api.example.com
  supacraft tenants disable shop --data-dir /var/lib/supacraft

SUPACRAFT_SERVER, SUPACRAFT_ADMIN_SECRET and SUPACRAFT_DATA_DIR may be set in
the environment or in the current project's .env.`,
}

var tenantsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List tenants",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		t, err := tenantsTarget()
		if err != nil {
			return err
		}
		var list []tenant.TenantInfo
		if t.client != nil {
			var out struct {
				Data []tenant.TenantInfo `json:"data"`
			}
			err = t.client.Do(cmd.Context(), http.MethodGet, "/admin/tenants", nil, &out)
			list = out.Data
		} else {
			list, err = tenant.ListTenants(cmd.Context(), t.dataDir)
		}
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Printf("No tenants on %s. Add one with: supacraft deploy\n", t.where)
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tTABLES\tFUNCTIONS\tCRONJOBS\tDB SIZE\tUPDATED")
		for _, t := range list {
			status := "enabled"
			if t.Disabled {
				status = "disabled"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\n", t.ID, t.Name, status, t.Tables, t.Functions, t.Cronjobs, humanBytes(t.DBBytes), t.UpdatedAt)
		}
		return w.Flush()
	},
}

var tenantsDisableCmd = &cobra.Command{
	Use:   "disable <id>",
	Short: "Stop serving a tenant (requests get 503, cron jobs pause); data is kept",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setDisabled(cmd.Context(), args[0], true) },
}

var tenantsEnableCmd = &cobra.Command{
	Use:   "enable <id>",
	Short: "Serve a disabled tenant again",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setDisabled(cmd.Context(), args[0], false) },
}

var tenantsRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove a tenant's config; --purge also deletes its database, files and secrets",
	Long: `Removes the tenant's config, so it is no longer served. Its database and
uploaded files stay, and deploying the project again brings it back.

With --purge, the database, secrets and uploaded files are deleted too,
including the uploads recorded in a remote storage driver (S3, GCS, Azure,
FTP, SFTP). This cannot be undone; you are asked to type the tenant id
unless --yes is given.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		t, err := tenantsTarget()
		if err != nil {
			return err
		}
		if tenantsPurge && !tenantsYes {
			fmt.Printf("This permanently deletes the database, secrets and files of %q on %s.\nType the tenant id to confirm: ", id, t.where)
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(line) != id {
				return errors.New("aborted")
			}
		}
		var res *tenant.RemoveResult
		if t.client != nil {
			path := "/admin/tenants/" + url.PathEscape(id)
			if tenantsPurge {
				path += "?purge=true"
			}
			res = &tenant.RemoveResult{}
			err = t.client.Do(cmd.Context(), http.MethodDelete, path, nil, res)
		} else {
			var cfg *config.Config
			if cfg, err = centralConfig(t.dataDir, "", ""); err == nil {
				res, err = tenant.RemoveTenant(cmd.Context(), t.dataDir, cfg.StoragePath, id, tenantsPurge)
			}
		}
		if err != nil {
			return tenantErr(id, err)
		}
		if !tenantsPurge {
			fmt.Printf("✓ Removed %s (database kept on %s)\n", id, t.where)
			return nil
		}
		fmt.Printf("✓ Removed %s and deleted its data\n", id)
		if res.FilesDriver != "" {
			fmt.Printf("  deleted %d uploaded file(s) from %s storage\n", res.FilesDeleted, res.FilesDriver)
		}
		return nil
	},
}

var tenantsErrorsCmd = &cobra.Command{
	Use:   "errors [id]",
	Short: "Show a tenant's recent server errors (default: the project in the current directory)",
	Long: `Shows the most recent unexpected errors (5xx) of a tenant, newest first, with
the internal error message. Deliberate fail responses and 4xx validation
errors are not logged. Secret values in messages are masked.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		t, err := tenantsTarget()
		if err != nil {
			return err
		}
		id := t.project
		if len(args) == 1 {
			id = args[0]
		}
		if id == "" {
			return errors.New("pass a tenant id or run in the project directory")
		}
		var out struct {
			Data  []errorlog.LogEntry `json:"data"`
			Total int                 `json:"total"`
		}
		if t.client != nil {
			err = t.client.Do(cmd.Context(), http.MethodGet, fmt.Sprintf("/admin/tenants/%s/errors?limit=%d", url.PathEscape(id), tenantsLimit), nil, &out)
		} else {
			out.Data, out.Total, err = localErrors(cmd.Context(), t.dataDir, id, tenantsLimit)
		}
		if err != nil {
			return tenantErr(id, err)
		}
		if len(out.Data) == 0 {
			fmt.Printf("No errors logged for %s on %s (errors are kept for 7 days)\n", id, t.where)
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TIME\tSTATUS\tREQUEST\tERROR")
		for _, e := range out.Data {
			fmt.Fprintf(w, "%s\t%d\t%s %s\t%s\n", e.Timestamp, e.StatusCode, e.Method, e.Path, oneLine(e.ErrorMessage))
		}
		if err := w.Flush(); err != nil {
			return err
		}
		if out.Total > len(out.Data) {
			fmt.Printf("(%d of %d; --limit shows more)\n", len(out.Data), out.Total)
		}
		return nil
	},
}

func init() {
	tenantsCmd.PersistentFlags().StringVar(&tenantsDataDir, "data-dir", "", "on the server: the central data directory (env SUPACRAFT_DATA_DIR)")
	tenantsCmd.PersistentFlags().StringVar(&tenantsServer, "server", "", "URL of the central server (env SUPACRAFT_SERVER)")
	tenantsRemoveCmd.Flags().BoolVar(&tenantsPurge, "purge", false, "also delete the database, secrets and uploaded files")
	tenantsRemoveCmd.Flags().BoolVarP(&tenantsYes, "yes", "y", false, "don't ask for confirmation")
	tenantsErrorsCmd.Flags().IntVar(&tenantsLimit, "limit", 30, "how many errors to show (max 500)")
	tenantsCmd.AddCommand(tenantsListCmd, tenantsDisableCmd, tenantsEnableCmd, tenantsRemoveCmd, tenantsErrorsCmd)
	rootCmd.AddCommand(tenantsCmd)
}

// tenantsTargetInfo is where tenants commands act: a local data directory or
// a remote server.
type tenantsTargetInfo struct {
	dataDir string
	client  *remote.Client
	where   string
	project string // tenant id of the project in the current directory, if any
}

// tenantsTarget picks --server, then --data-dir (or SUPACRAFT_DATA_DIR), then
// SUPACRAFT_SERVER from the environment or the project's .env.
func tenantsTarget() (*tenantsTargetInfo, error) {
	t := &tenantsTargetInfo{}
	env := map[string]string{}
	if p, err := loadProject(); err == nil {
		env = p.Env()
		t.project = p.TenantID()
	}
	lookup := func(k string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return env[k]
	}
	if tenantsServer == "" {
		if dir := firstNonEmpty(tenantsDataDir, os.Getenv("SUPACRAFT_DATA_DIR")); dir != "" {
			abs, err := filepath.Abs(dir)
			if err != nil {
				return nil, err
			}
			t.dataDir, t.where = abs, abs
			return t, nil
		}
	}
	server := firstNonEmpty(tenantsServer, lookup("SUPACRAFT_SERVER"))
	if server == "" {
		return nil, errors.New("pass --server <url> (or set SUPACRAFT_SERVER), or --data-dir on the server itself")
	}
	c, err := remote.New(server, lookup("SUPACRAFT_ADMIN_SECRET"))
	if err != nil {
		return nil, err
	}
	t.client, t.where = c, server
	return t, nil
}

func setDisabled(ctx context.Context, id string, disabled bool) error {
	t, err := tenantsTarget()
	if err != nil {
		return err
	}
	if t.client != nil {
		action := "enable"
		if disabled {
			action = "disable"
		}
		err = t.client.Do(ctx, http.MethodPost, "/admin/tenants/"+url.PathEscape(id)+"/"+action, nil, nil)
	} else {
		err = tenant.SetDisabled(t.dataDir, id, disabled)
	}
	if err != nil {
		return tenantErr(id, err)
	}
	if disabled {
		fmt.Printf("✓ Disabled %s on %s\n", id, t.where)
	} else {
		fmt.Printf("✓ Enabled %s on %s\n", id, t.where)
	}
	return nil
}

// localErrors reads errors.db in a data directory, masking secret values
// like the admin endpoint does.
func localErrors(ctx context.Context, dataDir, id string, limit int) ([]errorlog.LogEntry, int, error) {
	if !tenant.ValidTenantID(id) {
		return nil, 0, tenant.ErrTenantNotFound
	}
	if _, err := os.Stat(filepath.Join(dataDir, "errors.db")); errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	el, err := errorlog.Open(filepath.Join(dataDir, "errors.db"))
	if err != nil {
		return nil, 0, err
	}
	defer el.Close()
	entries, total, err := el.Query(ctx, id, min(max(limit, 1), 500), 0)
	if err != nil {
		return nil, 0, err
	}
	vals, err := secrets.New(dataDir).Values(id)
	if err != nil {
		return nil, 0, err
	}
	for i := range entries {
		entries[i].ErrorMessage = handler.MaskSecrets(entries[i].ErrorMessage, vals)
		entries[i].Path = handler.MaskSecrets(entries[i].Path, vals)
	}
	return entries, total, nil
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:197]) + "..."
	}
	return s
}

func tenantErr(id string, err error) error {
	var rerr *remote.Error
	if errors.Is(err, tenant.ErrTenantNotFound) || (errors.As(err, &rerr) && rerr.Status == http.StatusNotFound && rerr.Message == "server: no such tenant") {
		return fmt.Errorf("no tenant %q (see supacraft tenants list)", id)
	}
	return err
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
