package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/remote"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// errNoServer explains how to point the tools at a central server.
var errNoServer = errors.New("no central server configured: set SUPACRAFT_SERVER and SUPACRAFT_ADMIN_SECRET in the project's .env (the user does this; never ask for the secret in chat)")

// centralClient returns a signed client for the project's central server.
func (s *server) centralClient() (*remote.Client, *project.Project, string, error) {
	p, err := project.Load(s.opts.Dir)
	if err != nil {
		return nil, nil, "", err
	}
	srv := lookupEnv(p, "SUPACRAFT_SERVER")
	if srv == "" {
		return nil, nil, "", errNoServer
	}
	c, err := remote.New(srv, lookupEnv(p, "SUPACRAFT_ADMIN_SECRET"))
	return c, p, srv, err
}

// listTenants lists every tenant on the central server (read-only).
func (s *server) listTenants(ctx context.Context) (*mcp.CallToolResult, any, error) {
	c, p, srv, err := s.centralClient()
	if err != nil {
		return nil, nil, err
	}
	var out struct {
		Data []tenant.TenantInfo `json:"data"`
	}
	if err := c.Do(ctx, http.MethodGet, "/admin/tenants", nil, &out); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{
		"server":       srv,
		"this_project": p.TenantID(),
		"tenants":      out.Data,
		"note":         "Disabling or removing a tenant is done by the user with `supacraft tenants disable|enable|remove <id> --server " + srv + "`; suggest the command, don't run it.",
	}), nil, nil
}

// serverErrors reads the recent API errors of this project's tenant on the
// central server. Secret values are masked by the server.
func (s *server) serverErrors(ctx context.Context, limit int) (*mcp.CallToolResult, any, error) {
	c, p, srv, err := s.centralClient()
	if err != nil {
		return nil, nil, err
	}
	var out struct {
		Data  []errorlog.LogEntry `json:"data"`
		Total int                 `json:"total"`
	}
	path := fmt.Sprintf("/admin/tenants/%s/errors?limit=%d", url.PathEscape(p.TenantID()), min(max(limit, 1), 200))
	if err := c.Do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"server": srv, "tenant": p.TenantID(), "data": out.Data, "total": out.Total}), nil, nil
}
