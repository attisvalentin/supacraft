package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/attisvalentin/supacraft/internal/openapi"
	"github.com/spf13/cobra"
)

var (
	openapiOut       string
	openapiServerURL string
	openapiCentral   bool
)

var openapiCmd = &cobra.Command{
	Use:   "openapi",
	Short: "Print the project's API as an OpenAPI 3.1 document",
	Long: `Describes the API of supacraft.json as OpenAPI 3.1 JSON: the auth, CRUD and
file endpoints it enables and its pipeline functions, with request and
response schemas from the table columns and function inputs. Use it with
Swagger UI, Postman or a client generator.

  supacraft openapi -o openapi.json
  supacraft openapi --server-url https://api.example.com --central

A running server serves the same document at GET /meta/openapi.json.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := loadProject()
		if err != nil {
			return err
		}
		cfg, err := p.TenantConfig()
		if err != nil {
			return err
		}
		opts := openapi.Options{ServerURL: openapiServerURL}
		if opts.ServerURL == "" && !openapiCentral {
			opts.ServerURL = "http://localhost:8080"
		}
		if openapiCentral {
			opts.TenantHeader = p.TenantID()
		}
		data, err := json.MarshalIndent(openapi.Build(cfg, opts), "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		if openapiOut == "" || openapiOut == "-" {
			_, err = os.Stdout.Write(data)
			return err
		}
		if err := os.WriteFile(openapiOut, data, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "✓ Wrote %s\n", openapiOut)
		return nil
	},
}

func init() {
	openapiCmd.Flags().StringVarP(&openapiOut, "output", "o", "", "write to this file instead of stdout")
	openapiCmd.Flags().StringVar(&openapiServerURL, "server-url", "", "base URL clients call (default http://localhost:8080)")
	openapiCmd.Flags().BoolVar(&openapiCentral, "central", false, "for a central server: add the X-Tenant-ID header every request needs")
	rootCmd.AddCommand(openapiCmd)
}
