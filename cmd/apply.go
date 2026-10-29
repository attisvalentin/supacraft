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
applies automatically on every save.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := loadProject()
		if err != nil {
			return err
		}
		_, err = applyAndReport(cmd.Context(), p)
		return err
	},
}

func init() {
	rootCmd.AddCommand(validateCmd, applyCmd)
}

func applyAndReport(ctx context.Context, p *project.Project) (*project.ApplyResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	res, err := p.Apply(ctx)
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
		fmt.Printf("  ⚠ ${%s} is not set in .env\n", name)
	}
	fmt.Printf("✓ Applied %s\n", p.Name())
	return res, nil
}
