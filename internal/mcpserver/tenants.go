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
		"note":         "manage_tenant disables, enables or removes a tenant once the user approves; get_server_errors takes a tenant id.",
	}), nil, nil
}

// serverErrors reads the recent API errors of a tenant (default: this
// project's) on the central server. Secret values are masked by the server.
func (s *server) serverErrors(ctx context.Context, id string, limit int) (*mcp.CallToolResult, any, error) {
	c, p, srv, err := s.centralClient()
	if err != nil {
		return nil, nil, err
	}
	var out struct {
		Data  []errorlog.LogEntry `json:"data"`
		Total int                 `json:"total"`
	}
	if id == "" {
		id = p.TenantID()
	}
	path := fmt.Sprintf("/admin/tenants/%s/errors?limit=%d", url.PathEscape(id), min(max(limit, 1), 200))
	if err := c.Do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"server": srv, "tenant": id, "data": out.Data, "total": out.Total}), nil, nil
}
