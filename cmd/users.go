package cmd

import (
	"context"
	"errors"
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
	usersTenant string
	usersLocal  bool
	usersLimit  int
)

var usersCmd = &cobra.Command{
	Use:   "users",
	Short: "List a project's users and set their roles",
	Long: `List the users of a project and give them one of the roles defined in
api_settings.auth.roles, e.g. make the first admin:

  supacraft users set-role alice@example.com admin

Acts on the central server when --server, --data-dir, SUPACRAFT_SERVER or
SUPACRAFT_DATA_DIR is set (like "supacraft tenants"), otherwise, or with
--local, on the project's own database in .supacraft. The tenant defaults
to the project in the current directory.

A role change applies to the user's next request; tokens already issued
don't need to be renewed.`,
}

var usersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List users with their roles",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		t, id, err := projectTarget(usersTenant, usersLocal)
		if err != nil {
			return err
		}
		var out struct {
			Data  []tenant.UserInfo `json:"data"`
			Total int               `json:"total"`
		}
		if t.client != nil {
			err = t.client.Do(cmd.Context(), http.MethodGet, fmt.Sprintf("/admin/tenants/%s/users?limit=%d", url.PathEscape(id), usersLimit), nil, &out)
		} else {
			out.Data, out.Total, err = tenant.ListUsers(cmd.Context(), t.dataDir, id, min(max(usersLimit, 1), 500), 0)
		}
		if err != nil {
			return tenantErr(id, err)
		}
		if len(out.Data) == 0 {
			fmt.Printf("No users in %s on %s\n", id, t.where)
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "EMAIL\tROLE\tSTATUS\tVERIFIED\tCREATED")
		for _, u := range out.Data {
			role, status, verified := u.Role, "active", "no"
			if role == "" {
				role = "-"
			}
			if !u.Active {
				status = "inactive"
			}
			if u.Verified {
				verified = "yes"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", u.Email, role, status, verified, u.Created)
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

var usersSetRoleCmd = &cobra.Command{
	Use:   "set-role <email> <role>",
	Short: "Give a user one of the roles in api_settings.auth.roles",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		t, id, err := projectTarget(usersTenant, usersLocal)
		if err != nil {
			return err
		}
		user, err := setUserRole(cmd.Context(), t, id, args[0], args[1])
		if err != nil {
			return err
		}
		fmt.Printf("✓ %s is now %s in %s on %s\n", user.Email, user.Role, id, t.where)
		return nil
	},
}

func init() {
	usersCmd.PersistentFlags().StringVar(&tenantsDataDir, "data-dir", "", "on the server: the central data directory (env SUPACRAFT_DATA_DIR)")
	usersCmd.PersistentFlags().StringVar(&tenantsServer, "server", "", "URL of the central server (env SUPACRAFT_SERVER)")
	usersCmd.PersistentFlags().StringVar(&usersTenant, "tenant", "", "tenant id (default: the project in the current directory)")
	usersCmd.PersistentFlags().BoolVar(&usersLocal, "local", false, "use the project's own database in .supacraft even if a server is configured")
	usersListCmd.Flags().IntVar(&usersLimit, "limit", 100, "how many users to show (max 500)")
	usersCmd.AddCommand(usersListCmd, usersSetRoleCmd)
	rootCmd.AddCommand(usersCmd)
}

// projectTarget is tenantsTarget, falling back to (or with local, forcing)
// the project's .supacraft directory, plus the tenant id to act on (the
// project's unless tenantFlag names one).
func projectTarget(tenantFlag string, local bool) (*tenantsTargetInfo, string, error) {
	p, perr := loadProject()
	id := tenantFlag
	if id == "" && perr == nil {
		id = p.TenantID()
	}
	if id == "" {
		return nil, "", errors.New("pass --tenant <id> or run in the project directory")
	}
	useLocal := func() (*tenantsTargetInfo, string, error) {
		if perr != nil {
			return nil, "", perr
		}
		return &tenantsTargetInfo{dataDir: p.DataDir(), where: "the local project"}, id, nil
	}
	if local {
		if tenantsServer != "" || tenantsDataDir != "" {
			return nil, "", errors.New("--local can't be combined with --server or --data-dir")
		}
		return useLocal()
	}
	t, err := tenantsTarget()
	if errors.Is(err, errNoTarget) {
		return useLocal()
	}
	if err != nil {
		return nil, "", err
	}
	return t, id, nil
}

// setUserRole changes a user's role on the target.
func setUserRole(ctx context.Context, t *tenantsTargetInfo, id, email, role string) (*tenant.UserInfo, error) {
	if t.client != nil {
		var out struct {
			Data tenant.UserInfo `json:"data"`
		}
		err := t.client.Do(ctx, http.MethodPost, "/admin/tenants/"+url.PathEscape(id)+"/users/role", handler.SetRoleRequest{Email: email, Role: role}, &out)
		if err != nil {
			return nil, tenantErr(id, err)
		}
		return &out.Data, nil
	}
	user, err := tenant.SetUserRole(ctx, t.dataDir, id, email, role)
	if err != nil {
		return nil, tenantErr(id, err)
	}
	return user, nil
}
