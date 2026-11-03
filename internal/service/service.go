// Package service installs SupaCraft as a systemd service (Linux).
package service

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	name     = "supacraft"
	unitPath = "/etc/systemd/system/supacraft.service"
)

// Options say where command output goes and whether sudo may prompt for a
// password. Without a terminal (MCP) it must not: sudo then fails instead
// of waiting forever.
type Options struct {
	Out            io.Writer
	NonInteractive bool
}

func (o Options) cmd(root bool, name string, args ...string) *exec.Cmd {
	var c *exec.Cmd
	switch {
	case !root || os.Getuid() == 0:
		c = exec.Command(name, args...)
	case o.NonInteractive:
		c = exec.Command("sudo", append([]string{"-n", name}, args...)...)
	default:
		c = exec.Command("sudo", append([]string{name}, args...)...)
	}
	c.Stdout, c.Stderr = o.Out, o.Out
	return c
}

func linuxOnly() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd is only available on Linux")
	}
	return nil
}

// Install writes the unit for "serve <flag> <workDir>" (flag is --dir or
// --data-dir), then enables and starts it as the current user.
func Install(o Options, workDir, flag string) error {
	if err := linuxOnly(); err != nil {
		return err
	}
	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to detect binary path: %w", err)
	}
	if binPath, err = filepath.EvalSymlinks(binPath); err != nil {
		return fmt.Errorf("failed to resolve binary path: %w", err)
	}
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("failed to detect current user: %w", err)
	}
	unit, err := GenerateUnit(binPath, u.Username, u.HomeDir, workDir, flag)
	if err != nil {
		return err
	}
	tee := o.cmd(true, "tee", unitPath)
	tee.Stdin, tee.Stdout = strings.NewReader(unit), io.Discard
	if err := tee.Run(); err != nil {
		return fmt.Errorf("failed to write unit file: %w", err)
	}
	for _, args := range [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", name},
		{"systemctl", "start", name},
	} {
		if err := o.cmd(true, args[0], args[1:]...).Run(); err != nil {
			return fmt.Errorf("failed to run %s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

// Uninstall stops, disables and removes the service.
func Uninstall(o Options) error {
	if err := linuxOnly(); err != nil {
		return err
	}
	for _, args := range [][]string{{"systemctl", "stop", name}, {"systemctl", "disable", name}} {
		_ = o.cmd(true, args[0], args[1:]...).Run() // it may not be running
	}
	if err := o.cmd(true, "rm", "-f", unitPath).Run(); err != nil {
		return fmt.Errorf("failed to remove unit file: %w", err)
	}
	if err := o.cmd(true, "systemctl", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}
	return nil
}

// Status writes `systemctl status`. An inactive service is not an error.
func Status(o Options) error {
	if err := linuxOnly(); err != nil {
		return err
	}
	_ = o.cmd(false, "systemctl", "status", "--no-pager", name).Run()
	return nil
}

// GenerateUnit writes the systemd unit for "serve <flag> <workDir>". Paths
// are quoted for systemd: % and $ are escaped (systemd expands both), and
// values that can't be expressed in a unit line are refused.
func GenerateUnit(binPath, username, homeDir, workDir, flag string) (string, error) {
	for _, v := range []string{binPath, username, homeDir, workDir} {
		if v == "" || strings.ContainsAny(v, "\r\n\x00") || strings.HasSuffix(v, `\`) {
			return "", fmt.Errorf("%q cannot be used in a systemd unit", v)
		}
	}
	return fmt.Sprintf(`[Unit]
Description=SupaCraft API Server
After=network.target

[Service]
Type=simple
User=%s
WorkingDirectory=%s
ExecStart=%s serve %s %s
Restart=on-failure
RestartSec=5
Environment=%s

[Install]
WantedBy=multi-user.target
`, unitValue(username), unitValue(workDir), unitArg(binPath), flag, unitArg(workDir), unitArg("HOME="+homeDir)), nil
}

// unitValue escapes specifiers in a plain unit setting.
func unitValue(s string) string { return strings.ReplaceAll(s, "%", "%%") }

// unitArg quotes one ExecStart/Environment word.
func unitArg(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$").Replace(s) + `"`
}
