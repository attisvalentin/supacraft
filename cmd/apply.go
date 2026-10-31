package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/spf13/cobra"
)

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Check supacraft.json for errors",
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := loadProject()
		if err != nil {
			return err
		}
		issues := p.Validate()
		for _, i := range issues {
			fmt.Println(i)
		}
		if project.HasErrors(issues) {
			return errors.New("validation failed")
		}
		fmt.Println("✓ supacraft.json is valid")
		return nil
	},
}

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Migrate the database and write the compiled config",
	Long: `Validates supacraft.json, creates missing tables and columns in the SQLite
database (additive only) and writes the config the API server reads.
A running server picks the change up within 30 seconds; supacraft dev
applies automatically on every save.

With --data-dir, applies the project as one tenant of a central data
directory served by "supacraft serve --data-dir"; other tenants there are
left alone.

  supacraft apply --data-dir /var/lib/supacraft`,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := loadProject()
		if err != nil {
			return err
		}
		_, err = applyAndReport(cmd.Context(), p)
		return err
	},
}

var (
	applyDataDir string
	applyReplace bool
)

func init() {
	applyCmd.Flags().BoolVar(&applyReplace, "replace", false, "with --data-dir: replace a tenant with the same id from another project")
	applyCmd.Flags().StringVar(&applyDataDir, "data-dir", "", "apply into this central data directory instead of the project's .supacraft")
	rootCmd.AddCommand(validateCmd, applyCmd)
}

func applyAndReport(ctx context.Context, p *project.Project) (*project.ApplyResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var res *project.ApplyResult
	var err error
	if applyDataDir != "" {
		res, err = p.ApplyTo(ctx, applyDataDir, applyReplace)
	} else {
		res, err = p.Apply(ctx)
	}
	if err != nil {
		return nil, err
	}
	if res.Created {
		fmt.Printf("✓ Created database %s\n", res.Database)
	}
	for _, m := range res.Migrations {
		fmt.Printf("  • %s\n", m)
	}
	for _, name := range res.MissingEnv {
		if applyDataDir != "" {
			fmt.Printf("  ⚠ secret %s is not set on this server: supacraft secrets set %s --tenant %s --data-dir %s\n", name, name, res.TenantID, applyDataDir)
		} else {
			fmt.Printf("  ⚠ ${%s} is not set in .env\n", name)
		}
	}
	if applyDataDir != "" {
		fmt.Printf("✓ Applied %s as tenant %q in %s\n", p.Name(), res.TenantID, applyDataDir)
		return res, nil
	}
	fmt.Printf("✓ Applied %s\n", p.Name())
	return res, nil
}
