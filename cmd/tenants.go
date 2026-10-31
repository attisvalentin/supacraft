package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/spf13/cobra"
)

var (
	tenantsDataDir string
	tenantsPurge   bool
	tenantsYes     bool
)

var tenantsCmd = &cobra.Command{
	Use:   "tenants",
	Short: "List and manage the tenants in a central data directory",
	Long: `Manage the tenants served by "supacraft serve --data-dir". A running
server picks every change up within a few seconds.

  supacraft tenants list --data-dir /var/lib/supacraft
  supacraft tenants disable shop --data-dir /var/lib/supacraft`,
}

var tenantsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List tenants",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := tenantsDir()
		if err != nil {
			return err
		}
		list, err := tenant.ListTenants(cmd.Context(), dir)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Printf("No tenants in %s. Add one with: supacraft apply --data-dir %s\n", dir, dir)
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
	RunE:  func(cmd *cobra.Command, args []string) error { return setDisabled(args[0], true) },
}

var tenantsEnableCmd = &cobra.Command{
	Use:   "enable <id>",
	Short: "Serve a disabled tenant again",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setDisabled(args[0], false) },
}

var tenantsRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove a tenant's config; --purge also deletes its database and files",
	Long: `Removes the tenant's config, so it is no longer served. Its database and
uploaded files stay, and applying the project again brings it back.

With --purge, the database and uploaded files are deleted too. This cannot
be undone; you are asked to type the tenant id unless --yes is given.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		dir, err := tenantsDir()
		if err != nil {
			return err
		}
		if tenantsPurge && !tenantsYes {
			fmt.Printf("This permanently deletes the database and files of %q in %s.\nType the tenant id to confirm: ", id, dir)
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(line) != id {
				return errors.New("aborted")
			}
		}
		cfg, err := centralConfig(dir, "", "")
		if err != nil {
			return err
		}
		if err := tenant.RemoveTenant(cmd.Context(), dir, cfg.StoragePath, id, tenantsPurge); err != nil {
			return tenantErr(id, err)
		}
		if tenantsPurge {
			fmt.Printf("✓ Removed %s and deleted its data\n", id)
		} else {
			fmt.Printf("✓ Removed %s (database kept in %s)\n", id, dir)
		}
		return nil
	},
}

func init() {
	tenantsCmd.PersistentFlags().StringVar(&tenantsDataDir, "data-dir", os.Getenv("SUPACRAFT_DATA_DIR"), "central data directory (env SUPACRAFT_DATA_DIR)")
	tenantsRemoveCmd.Flags().BoolVar(&tenantsPurge, "purge", false, "also delete the database and uploaded files")
	tenantsRemoveCmd.Flags().BoolVarP(&tenantsYes, "yes", "y", false, "don't ask for confirmation")
	tenantsCmd.AddCommand(tenantsListCmd, tenantsDisableCmd, tenantsEnableCmd, tenantsRemoveCmd)
	rootCmd.AddCommand(tenantsCmd)
}

func tenantsDir() (string, error) {
	if tenantsDataDir == "" {
		return "", errors.New("--data-dir (or SUPACRAFT_DATA_DIR) is required")
	}
	return filepath.Abs(tenantsDataDir)
}

func setDisabled(id string, disabled bool) error {
	dir, err := tenantsDir()
	if err != nil {
		return err
	}
	if err := tenant.SetDisabled(dir, id, disabled); err != nil {
		return tenantErr(id, err)
	}
	if disabled {
		fmt.Printf("✓ Disabled %s\n", id)
	} else {
		fmt.Printf("✓ Enabled %s\n", id)
	}
	return nil
}

func tenantErr(id string, err error) error {
	if errors.Is(err, tenant.ErrTenantNotFound) {
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
