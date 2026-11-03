package mcpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/attisvalentin/supacraft/internal/handler"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/remote"
	"github.com/attisvalentin/supacraft/internal/service"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/attisvalentin/supacraft/internal/testrun"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tools here do what the operator CLI does (deploy, tenants, secrets,
// seed --reset, service, init). Changes to a central server and anything
// destructive need the user's approval in a prompt the agent can't answer.

// deploy sends supacraft.json to the central server. A dry run needs no
// approval; a real deploy is approved for exactly this project content.
func (s *server) deploy(ctx context.Context, req *mcp.CallToolRequest, dryRun, replace, allowMissing bool) (*mcp.CallToolResult, any, error) {
	p, err := project.Load(s.opts.Dir)
	if err != nil {
		return nil, nil, err
	}
	if issues := p.Validate(); project.HasErrors(issues) {
		return jsonResult(map[string]any{"deployed": false, "reason": "fix supacraft.json first", "issues": issues}), nil, nil
	}
	c, _, srv, err := s.centralClient()
	if err != nil {
		return nil, nil, err
	}
	payload := p.WithoutLocalSections()
	if !dryRun {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, err
		}
		sum := sha256.Sum256(data)
		flags := []string{"deploy"}
		if replace {
			flags = append(flags, "--replace")
		}
		if allowMissing {
			flags = append(flags, "--allow-missing-secrets")
		}
		if res, ok := approved(req, approval{
			action:  []string{"deploy", srv, p.TenantID(), hex.EncodeToString(sum[:]), fmt.Sprint(replace), fmt.Sprint(allowMissing)},
			message: fmt.Sprintf("An AI agent wants to deploy %s (the current supacraft.json) to %s (production). Run a dry run first if you haven't seen what changes. Approve only if you asked for this.", p.TenantID(), srv),
			title:   fmt.Sprintf("Deploy %s to %s", p.TenantID(), srv),
			command: shellCommand(append([]string{"supacraft"}, append(flags, "--server="+srv)...)...),
		}); !ok {
			return res, nil, nil
		}
	}
	var out handler.DeployResponse
	err = c.Do(ctx, http.MethodPut, "/admin/tenants/"+url.PathEscape(p.TenantID()), handler.DeployRequest{
		Project: payload, Replace: replace, DryRun: dryRun, AllowMissingSecrets: allowMissing,
	}, &out)
	var rerr *remote.Error
	if errors.As(err, &rerr) && (len(rerr.MissingSecrets) > 0 || len(rerr.Issues) > 0) {
		res := map[string]any{"deployed": false, "reason": rerr.Message}
		if len(rerr.MissingSecrets) > 0 {
			res["missing_secrets"] = rerr.MissingSecrets
			res["note"] = "The user sets each with `supacraft secrets set NAME --server=" + srv + "` (hidden input); never ask for the value in chat. Then deploy again."
		}
		if len(rerr.Issues) > 0 {
			res["issues"] = rerr.Issues
		}
		return jsonResult(res), nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"server": srv, "result": out}), nil, nil
}

// tenantAction disables, enables or removes a tenant on the central server
// after the user approves.
func (s *server) tenantAction(ctx context.Context, req *mcp.CallToolRequest, id, action string, purge bool) (*mcp.CallToolResult, any, error) {
	c, p, srv, err := s.centralClient()
	if err != nil {
		return nil, nil, err
	}
	if id == "" {
		id = p.TenantID()
	}
	var message, title string
	cmd := []string{"supacraft", "tenants", action, id}
	switch {
	case action == "remove" && purge:
		message = fmt.Sprintf("An AI agent wants to remove %s from %s and PERMANENTLY DELETE its database, uploaded files and secrets. This cannot be undone. Approve only if you asked for this.", id, srv)
		title = "Permanently delete " + id + " and all its data"
		cmd = append(cmd, "--purge")
	case action == "remove":
		message = fmt.Sprintf("An AI agent wants to remove %s from %s: it stops being served; its database stays and a deploy brings it back. Approve only if you asked for this.", id, srv)
		title = "Remove " + id
	case action == "disable":
		message = fmt.Sprintf("An AI agent wants to disable %s on %s: every request gets 503 and its cron jobs pause until it is enabled. Approve only if you asked for this.", id, srv)
		title = "Disable " + id
	case action == "enable":
		message = fmt.Sprintf("An AI agent wants to enable %s on %s again. Approve only if you asked for this.", id, srv)
		title = "Enable " + id
	default:
		return nil, nil, fmt.Errorf("unknown action %q", action)
	}
	if res, ok := approved(req, approval{
		action:  []string{"tenant", action, srv, id, fmt.Sprint(purge)},
		message: message, title: title,
		command: shellCommand(append(cmd, "--server="+srv)...),
	}); !ok {
		return res, nil, nil
	}
	path := "/admin/tenants/" + url.PathEscape(id)
	var out any
	switch action {
	case "remove":
		if purge {
			path += "?purge=true"
		}
		var res tenant.RemoveResult
		err = c.Do(ctx, http.MethodDelete, path, nil, &res)
		out = res
	default:
		err = c.Do(ctx, http.MethodPost, path+"/"+action, nil, nil)
		out = map[string]string{"tenant": id, "status": action + "d"}
	}
	if err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"server": srv, "result": out}), nil, nil
}

// unsetSecret removes a secret of this project's tenant on the central
// server after the user approves. (Setting one stays with the user: its
// value must not pass through the agent.)
func (s *server) unsetSecret(ctx context.Context, req *mcp.CallToolRequest, name string) (*mcp.CallToolResult, any, error) {
	c, p, srv, err := s.centralClient()
	if err != nil {
		return nil, nil, err
	}
	id := p.TenantID()
	if res, ok := approved(req, approval{
		action:  []string{"unset_secret", srv, id, name},
		message: fmt.Sprintf("An AI agent wants to delete the secret %s of %s on %s; \"${%s}\" then resolves to \"\" until it is set again. Approve only if you asked for this.", name, id, srv, name),
		title:   "Delete secret " + name,
		command: shellCommand("supacraft", "secrets", "unset", name, "--server="+srv),
	}); !ok {
		return res, nil, nil
	}
	path := "/admin/tenants/" + url.PathEscape(id) + "/secrets/" + url.PathEscape(name)
	if err := c.Do(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"server": srv, "tenant": id, "unset": name}), nil, nil
}

// seed loads the seed data; reset (deleting every local row first) needs
// the user's approval.
func (s *server) seed(ctx context.Context, req *mcp.CallToolRequest, reset bool) (*mcp.CallToolResult, any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := project.Load(s.opts.Dir)
	if err != nil {
		return nil, nil, err
	}
	if reset {
		if res, ok := approved(req, approval{
			action:  []string{"seed_reset", p.Dir, p.TenantID()},
			message: fmt.Sprintf("An AI agent wants to delete every row in the local database of %s (users included) and load the seed data. Approve only if you asked for this.", p.TenantID()),
			title:   "Reset the local database of " + p.TenantID(),
			command: "supacraft seed --reset",
		}); !ok {
			return res, nil, nil
		}
	}
	res, err := testrun.SeedLocal(ctx, p, reset)
	if err != nil {
		return nil, nil, err
	}
	if s.opts.Reload != nil {
		s.opts.Reload(ctx)
	}
	return jsonResult(res), nil, nil
}

// serviceAction runs `supacraft service status|install|uninstall` on this
// machine; install and uninstall need the user's approval, and sudo must
// not need a password (there is no terminal to type it).
func (s *server) serviceAction(ctx context.Context, req *mcp.CallToolRequest, action, dataDir string) (*mcp.CallToolResult, any, error) {
	var out bytes.Buffer
	opts := service.Options{Out: &out, NonInteractive: true}
	var err error
	switch action {
	case "status":
		err = service.Status(opts)
	case "install", "uninstall":
		workDir, flag := dataDir, "--data-dir"
		cmd := []string{"supacraft", "service", action}
		if action == "install" {
			if dataDir == "" {
				workDir, flag = s.opts.Dir, "--dir"
			} else {
				cmd = append(cmd, "--data-dir="+dataDir)
			}
		}
		if res, ok := approved(req, approval{
			action:  []string{"service", action, workDir, flag},
			message: fmt.Sprintf("An AI agent wants to %s the SupaCraft systemd service on this machine (needs sudo). Approve only if you asked for this.", action),
			title:   fmt.Sprintf("%s the systemd service", action),
			command: shellCommand(cmd...),
		}); !ok {
			return res, nil, nil
		}
		if action == "install" {
			err = service.Install(opts, workDir, flag)
		} else {
			err = service.Uninstall(opts)
		}
	default:
		return nil, nil, fmt.Errorf("unknown action %q", action)
	}
	if err != nil {
		return jsonResult(map[string]any{"ok": false, "error": err.Error(), "output": out.String(),
			"note": "If sudo needs a password, the user runs the command in a terminal."}), nil, nil
	}
	return jsonResult(map[string]any{"ok": true, "output": out.String()}), nil, nil
}

// initProject creates a project in the MCP server's directory when it has
// none yet, and starts its API.
func (s *server) initProject(ctx context.Context, name string, auth bool) (*mcp.CallToolResult, any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" {
		return nil, nil, errors.New("name is required")
	}
	if err := project.Init(s.opts.Dir, name, auth); err != nil {
		return nil, nil, err
	}
	if s.opts.Started != nil {
		reload, err := s.opts.Started(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("created the project, but the API did not start: %w", err)
		}
		s.opts.Reload, s.opts.Started = reload, nil
	}
	return jsonResult(map[string]any{"created": s.opts.Dir, "name": name, "auth_enabled": auth,
		"next": "Build it with set_table / set_function / update_api_settings; the API serves it at " + s.opts.APIURL}), nil, nil
}
