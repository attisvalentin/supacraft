package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/attisvalentin/supacraft/internal/testrun"
	"github.com/spf13/cobra"
)

var (
	seedReset bool
	seedYes   bool
)

var seedCmd = &cobra.Command{
	Use:   "seed",
	Short: "Load the seed data into the local database",
	Long: `Adds the "seed" section of supacraft.json (users and table rows) to the
project's own database in .supacraft. It can run again: users already there
(by email) and rows already there (by primary key) are left as they are.

With --reset, every row of the project's tables is deleted first, so the
database holds exactly the seed. Seed data is never deployed to a server.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := loadProject()
		if err != nil {
			return err
		}
		if seedReset && !seedYes {
			fmt.Print("This deletes every row in the local database of this project. Continue? [y/N] ")
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
				return errors.New("aborted")
			}
		}
		res, err := testrun.SeedLocal(cmd.Context(), p, seedReset)
		if err != nil {
			return err
		}
		fmt.Printf("✓ Seeded: %d user(s)", res.Users)
		tables := make([]string, 0, len(res.Rows))
		for t := range res.Rows {
			tables = append(tables, t)
		}
		sort.Strings(tables)
		for _, t := range tables {
			fmt.Printf(", %d %s", res.Rows[t], t)
		}
		fmt.Println()
		for _, t := range res.Skipped {
			fmt.Printf("  %s already has rows and no primary key, so its seed rows were skipped (use --reset)\n", t)
		}
		return nil
	},
}

var testCmd = &cobra.Command{
	Use:   "test [name...]",
	Short: "Run the tests of supacraft.json",
	Long: `Runs the "tests" section of supacraft.json. Each test sends one request to
the API on its own fresh, seeded database in a temporary directory, so the
local data is never touched, and checks the response status and body, the
rows in the database and the emails sent.

Tests have no outside effects: emails are captured, not sent, and
http.request steps get the test's http_mocks; a call without a mock fails.
Exits non-zero when a test fails, so it fits CI.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := loadProject()
		if err != nil {
			return err
		}
		// The per-request log lines would bury the results.
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
		results, err := testrun.Run(cmd.Context(), p, args...)
		if err != nil {
			return err
		}
		if len(results) == 0 {
			fmt.Println("No tests. Add them under \"tests\" in supacraft.json.")
			return nil
		}
		failed := 0
		for _, r := range results {
			if r.Passed {
				fmt.Printf("✓ %s\n", r.Name)
				continue
			}
			failed++
			fmt.Printf("✗ %s (status %d)\n", r.Name, r.Status)
			for _, f := range r.Failures {
				fmt.Printf("    %s\n", f)
			}
			for _, e := range r.ServerErrors {
				fmt.Printf("    server error: %s\n", e)
			}
			if r.Body != "" {
				fmt.Printf("    response: %s\n", r.Body)
			}
		}
		fmt.Printf("\n%d passed, %d failed\n", len(results)-failed, failed)
		if failed > 0 {
			cmd.SilenceUsage = true
			return fmt.Errorf("%d test(s) failed", failed)
		}
		return nil
	},
}

func init() {
	seedCmd.Flags().BoolVar(&seedReset, "reset", false, "delete every row first, so the database holds exactly the seed")
	seedCmd.Flags().BoolVarP(&seedYes, "yes", "y", false, "don't ask before --reset")
	rootCmd.AddCommand(seedCmd, testCmd)
}
