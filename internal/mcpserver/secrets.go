package mcpserver

import (
	"context"
	"net/http"
	"net/url"
	"os"

	"github.com/attisvalentin/supacraft/internal/envref"
	"github.com/attisvalentin/supacraft/internal/handler"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/remote"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// secretsNote is returned with every check_secrets result.
const secretsNote = "Never ask the user to paste a secret value into the chat. Give them the command; they type the value into the hidden prompt themselves."

// checkSecrets reports, by name only, which "${NAME}" references are set:
// on the central server when SUPACRAFT_SERVER is configured, otherwise in
// the project's .env.
func (s *server) checkSecrets(ctx context.Context) (*mcp.CallToolResult, any, error) {
	p, err := project.Load(s.opts.Dir)
	if err != nil {
		return nil, nil, err
	}
	env := p.Env()
	referenced := envref.Names(p.Raw["api_settings"])

	if server := lookupEnv(p, "SUPACRAFT_SERVER"); server != "" {
		c, err := remote.New(server, lookupEnv(p, "SUPACRAFT_ADMIN_SECRET"))
		if err != nil {
			return nil, nil, err
		}
		var st handler.SecretsStatus
		if err := c.Do(ctx, http.MethodGet, "/admin/tenants/"+url.PathEscape(p.TenantID())+"/secrets", nil, &st); err != nil {
			return nil, nil, err
		}
		have := map[string]bool{}
		for _, n := range st.Set {
			have[n] = true
		}
		missing := []string{}
		commands := []string{}
		for _, n := range referenced {
			if !have[n] {
				missing = append(missing, n)
				commands = append(commands, "supacraft secrets set "+n)
			}
		}
		return jsonResult(map[string]any{
			"where":      "central server " + server + " (tenant " + p.TenantID() + ")",
			"referenced": nonNilStrings(referenced),
			"set":        nonNilStrings(st.Set),
			"missing":    missing,
			"how_to_set": "The user runs each command in the project directory (in Claude Code: prefix it with !). It asks for the value with hidden input and sends it straight to the server.",
			"commands":   commands,
			"note":       secretsNote,
		}), nil, nil
	}

	missing := []string{}
	for _, n := range referenced {
		if v, ok := env[n]; !ok || v == "" {
			if os.Getenv(n) == "" {
				missing = append(missing, n)
			}
		}
	}
	return jsonResult(map[string]any{
		"where":      "local .env in " + p.Dir,
		"referenced": nonNilStrings(referenced),
		"missing":    missing,
		"how_to_set": "The user adds NAME=value lines for the missing names to the project's .env file in an editor (it is git-ignored). On a central server, secrets are set with `supacraft secrets set NAME --server <url>` instead.",
		"note":       secretsNote,
	}), nil, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// lookupEnv reads k from the environment, then from the project's .env.
func lookupEnv(p *project.Project, k string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return p.Env()[k]
}
