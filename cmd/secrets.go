package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/attisvalentin/supacraft/internal/handler"
	"github.com/attisvalentin/supacraft/internal/remote"
	"github.com/attisvalentin/supacraft/internal/secrets"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	secretsDataDir string
	secretsServer  string
	secretsTenant  string
)

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Manage the secrets of a tenant on a central server",
	Long: `Secrets are the values behind "${NAME}" references in supacraft.json. On a
central server (serve --data-dir) they live only on the server, in
<data-dir>/env/<tenant>.json, and never in the project or a deploy.

Run these on your machine against the server:

  supacraft secrets set STRIPE_KEY --server https://api.example.com
  supacraft secrets list --server https://api.example.com

or on the server itself with --data-dir. The tenant is the project in the
current directory (or --tenant). Remote calls are signed with
SUPACRAFT_ADMIN_SECRET; SUPACRAFT_SERVER and SUPACRAFT_ADMIN_SECRET may be
set in the environment or in the project's .env.

The value is read from a hidden prompt, or from stdin when piped. It is never
a command-line argument, so it doesn't end up in your shell history.`,
}

var secretsSetCmd = &cobra.Command{
	Use:   "set NAME",
	Short: "Set a secret (the value is read from a hidden prompt or stdin)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		t, err := secretsTarget()
		if err != nil {
			return err
		}
		value, err := readSecretValue(name)
		if err != nil {
			return err
		}
		if t.client != nil {
			err = t.client.Do(cmd.Context(), http.MethodPut, secretPath(t.tenant, name), map[string]string{"value": value}, nil)
		} else {
			err = secrets.New(t.dataDir).Set(t.tenant, name, value)
		}
		if err != nil {
			return err
		}
		fmt.Printf("✓ Set %s for %s on %s\n", name, t.tenant, t.where)
		return nil
	},
}

var secretsUnsetCmd = &cobra.Command{
	Use:   "unset NAME",
	Short: "Remove a secret",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		t, err := secretsTarget()
		if err != nil {
			return err
		}
		if t.client != nil {
			err = t.client.Do(cmd.Context(), http.MethodDelete, secretPath(t.tenant, name), nil, nil)
		} else {
			err = secrets.New(t.dataDir).Unset(t.tenant, name)
		}
		if err != nil {
			return err
		}
		fmt.Printf("✓ Removed %s for %s on %s\n", name, t.tenant, t.where)
		return nil
	},
}

var secretsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List secret names: which are set and which the deployed config still needs (values are never shown)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		t, err := secretsTarget()
		if err != nil {
			return err
		}
		st, err := t.status(cmd.Context())
		if err != nil {
			return err
		}
		printSecretsStatus(st, t)
		return nil
	},
}

func init() {
	secretsCmd.PersistentFlags().StringVar(&secretsDataDir, "data-dir", "", "on the server: the central data directory")
	secretsCmd.PersistentFlags().StringVar(&secretsServer, "server", "", "URL of the central server (env SUPACRAFT_SERVER)")
	secretsCmd.PersistentFlags().StringVar(&secretsTenant, "tenant", "", "tenant id (default: the project in the current directory)")
	secretsCmd.AddCommand(secretsSetCmd, secretsUnsetCmd, secretsListCmd)
	rootCmd.AddCommand(secretsCmd)
}

// secretsTargetInfo is where secrets commands act: a local data directory or
// a remote server.
type secretsTargetInfo struct {
	tenant  string
	dataDir string
	client  *remote.Client
	where   string // for messages
	server  string // remote URL, for suggested commands
}

func (t *secretsTargetInfo) status(ctx context.Context) (*handler.SecretsStatus, error) {
	if t.client == nil {
		return handler.SecretsStatusFor(t.dataDir, t.tenant)
	}
	var st handler.SecretsStatus
	err := t.client.Do(ctx, http.MethodGet, "/admin/tenants/"+url.PathEscape(t.tenant)+"/secrets", nil, &st)
	return &st, err
}

func secretPath(tenant, name string) string {
	return "/admin/tenants/" + url.PathEscape(tenant) + "/secrets/" + url.PathEscape(name)
}

func secretsTarget() (*secretsTargetInfo, error) {
	// The project in --dir supplies the default tenant and, from its .env,
	// the server URL and admin secret. It is optional with --tenant.
	env := map[string]string{}
	t := &secretsTargetInfo{tenant: secretsTenant}
	if p, err := loadProject(); err == nil {
		env = p.Env()
		if t.tenant == "" {
			t.tenant = p.TenantID()
		}
	}
	if t.tenant == "" {
		return nil, errors.New("no project here: pass --tenant <id> or run in the project directory")
	}
	lookup := func(k string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return env[k]
	}

	if dir := firstNonEmpty(secretsDataDir, os.Getenv("SUPACRAFT_DATA_DIR")); dir != "" && secretsServer == "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		t.dataDir, t.where = abs, abs
		return t, nil
	}
	server := firstNonEmpty(secretsServer, lookup("SUPACRAFT_SERVER"))
	if server == "" {
		return nil, errors.New("pass --server <url> (or set SUPACRAFT_SERVER), or --data-dir on the server itself")
	}
	c, err := remote.New(server, lookup("SUPACRAFT_ADMIN_SECRET"))
	if err != nil {
		return nil, err
	}
	t.client, t.where, t.server = c, server, server
	return t, nil
}

func readSecretValue(name string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprintf(os.Stderr, "Value for %s (input hidden): ", name)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if len(b) == 0 {
			return "", errors.New("empty value (use secrets unset to remove a secret)")
		}
		return string(b), nil
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, secrets.MaxValueBytes+2))
	if err != nil {
		return "", err
	}
	// Piped values usually end with one newline (echo, files); keep the rest.
	v := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if v == "" {
		return "", errors.New("empty value on stdin")
	}
	return v, nil
}

func printSecretsStatus(st *handler.SecretsStatus, t *secretsTargetInfo) {
	fmt.Printf("Tenant %s on %s\n", st.Tenant, t.where)
	if !st.Deployed {
		fmt.Println("  (not deployed yet: referenced names are known after the first apply/deploy)")
	}
	set := map[string]bool{}
	for _, n := range st.Set {
		set[n] = true
	}
	referenced := map[string]bool{}
	for _, n := range st.Referenced {
		referenced[n] = true
		mark := "✓"
		if !set[n] {
			mark = "✗ missing"
		}
		fmt.Printf("  %-28s %s\n", n, mark)
	}
	for _, n := range st.Set {
		if !referenced[n] {
			fmt.Printf("  %-28s ✓ (not referenced)\n", n)
		}
	}
	for _, n := range st.Missing {
		fmt.Printf("\n  set it with: %s\n", setCommand(n, t))
	}
}

func setCommand(name string, t *secretsTargetInfo) string {
	if t.server != "" {
		return fmt.Sprintf("supacraft secrets set %s --server %s", name, t.server)
	}
	return fmt.Sprintf("supacraft secrets set %s --data-dir %s", name, t.dataDir)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
