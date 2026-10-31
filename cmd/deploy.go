package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/attisvalentin/supacraft/internal/handler"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/remote"
	"github.com/spf13/cobra"
)

var (
	deployServer       string
	deployReplace      bool
	deployDryRun       bool
	deployAllowMissing bool
)

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy this project to a central server",
	Long: `Sends supacraft.json to a central server (serve --data-dir). The server
validates it, creates or migrates the tenant's database (additive only) and
serves the new version within seconds, without a restart.

Only supacraft.json is sent: secret values stay on the server (see
"supacraft secrets"). If a "${NAME}" reference has no secret on the server,
nothing is changed and the missing names are listed.

  supacraft deploy --server https://api.example.com
  supacraft deploy --dry-run        # show what would change

SUPACRAFT_SERVER and SUPACRAFT_ADMIN_SECRET may be set in the environment or
in the project's .env.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := loadProject()
		if err != nil {
			return err
		}
		issues := p.Validate()
		if project.HasErrors(issues) {
			for _, i := range issues {
				fmt.Println(i)
			}
			return errors.New("fix supacraft.json before deploying")
		}

		env := p.Env()
		lookup := func(k string) string { return firstNonEmpty(envOr(k, ""), env[k]) }
		server := firstNonEmpty(deployServer, lookup("SUPACRAFT_SERVER"))
		c, err := remote.New(server, lookup("SUPACRAFT_ADMIN_SECRET"))
		if err != nil {
			return err
		}

		var res handler.DeployResponse
		err = c.Do(cmd.Context(), http.MethodPut, "/admin/tenants/"+url.PathEscape(p.TenantID()), handler.DeployRequest{
			Project:             p.Raw,
			Replace:             deployReplace,
			DryRun:              deployDryRun,
			AllowMissingSecrets: deployAllowMissing,
		}, &res)
		var rerr *remote.Error
		if errors.As(err, &rerr) && len(rerr.MissingSecrets) > 0 {
			fmt.Printf("✗ Not deployed: %s needs secrets that aren't set on %s:\n", p.TenantID(), server)
			for _, n := range rerr.MissingSecrets {
				fmt.Printf("    supacraft secrets set %s\n", n)
			}
			fmt.Println("  Run these (the value is asked with hidden input), then deploy again.")
			return errors.New("missing secrets")
		}
		if errors.As(err, &rerr) && len(rerr.Issues) > 0 {
			for _, i := range rerr.Issues {
				fmt.Println(i)
			}
		}
		if err != nil {
			return err
		}

		verb := "Deployed"
		if res.DryRun {
			verb = "Dry run: would deploy"
		}
		if res.Created {
			fmt.Printf("✓ %s new tenant %s on %s\n", verb, res.Tenant, server)
		} else {
			fmt.Printf("✓ %s %s on %s\n", verb, res.Tenant, server)
		}
		for _, m := range res.Migrations {
			fmt.Printf("  • %s\n", m)
		}
		for _, w := range res.Warnings {
			fmt.Printf("  ⚠ %s\n", w)
		}
		for _, n := range res.MissingSecrets {
			fmt.Printf("  ⚠ secret %s is not set (resolves to \"\"): supacraft secrets set %s\n", n, n)
		}
		if !res.DryRun {
			fmt.Printf("  Call it with the header X-Tenant-ID: %s\n", res.Tenant)
		}
		return nil
	},
}

func init() {
	deployCmd.Flags().StringVar(&deployServer, "server", "", "URL of the central server (env SUPACRAFT_SERVER)")
	deployCmd.Flags().BoolVar(&deployReplace, "replace", false, "replace a tenant with the same id from another project")
	deployCmd.Flags().BoolVar(&deployDryRun, "dry-run", false, "validate and show the migration without changing anything")
	deployCmd.Flags().BoolVar(&deployAllowMissing, "allow-missing-secrets", false, "deploy even if secrets are missing (they resolve to \"\" until set)")
	rootCmd.AddCommand(deployCmd)
}
