package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

const serviceName = "supacraft"
const unitPath = "/etc/systemd/system/supacraft.service"

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage the SupaCraft systemd service",
	Long:  "Install, uninstall, or check the status of the SupaCraft systemd service on Linux.",
}

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install and start the SupaCraft systemd service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceInstall()
	},
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop and remove the SupaCraft systemd service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceUninstall()
	},
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the status of the SupaCraft systemd service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceStatus()
	},
}

func init() {
	serviceCmd.AddCommand(serviceInstallCmd)
	serviceCmd.AddCommand(serviceUninstallCmd)
	serviceCmd.AddCommand(serviceStatusCmd)
	rootCmd.AddCommand(serviceCmd)
}

func runServiceInstall() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd is only available on Linux")
	}

	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to detect binary path: %w", err)
	}
	binPath, err = filepath.EvalSymlinks(binPath)
	if err != nil {
		return fmt.Errorf("failed to resolve binary path: %w", err)
	}

	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("failed to detect current user: %w", err)
	}

	p, err := loadProject()
	if err != nil {
		return err
	}
	workDir := p.Dir

	unit := generateUnit(binPath, u.Username, u.HomeDir, workDir)

	// Write unit file — direct if root, otherwise via sudo tee
	if os.Getuid() == 0 {
		if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
			return fmt.Errorf("failed to write unit file: %w", err)
		}
	} else {
		tee := sudoCmd("tee", unitPath)
		tee.Stdin = strings.NewReader(unit)
		tee.Stderr = os.Stderr
		if err := tee.Run(); err != nil {
			return fmt.Errorf("failed to write unit file: %w", err)
		}
	}

	// Reload, enable, start
	cmds := [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", serviceName},
		{"systemctl", "start", serviceName},
	}
	for _, args := range cmds {
		c := maybeRootCmd(args[0], args[1:]...)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("failed to run %s: %w", strings.Join(args, " "), err)
		}
	}

	fmt.Println("\n✓ SupaCraft service installed and started")
	fmt.Println("\nUseful commands:")
	fmt.Println("  sudo systemctl status supacraft    — check status")
	fmt.Println("  sudo systemctl restart supacraft   — restart")
	fmt.Println("  sudo systemctl stop supacraft      — stop")
	fmt.Println("  sudo journalctl -u supacraft -f    — follow logs")
	return nil
}

func runServiceUninstall() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd is only available on Linux")
	}

	cmds := [][]string{
		{"systemctl", "stop", serviceName},
		{"systemctl", "disable", serviceName},
	}
	for _, args := range cmds {
		c := maybeRootCmd(args[0], args[1:]...)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		// Ignore errors — service may not be running
		_ = c.Run()
	}

	if os.Getuid() == 0 {
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove unit file: %w", err)
		}
	} else {
		if err := sudoCmd("rm", "-f", unitPath).Run(); err != nil {
			return fmt.Errorf("failed to remove unit file: %w", err)
		}
	}

	reload := maybeRootCmd("systemctl", "daemon-reload")
	reload.Stdout = os.Stdout
	reload.Stderr = os.Stderr
	if err := reload.Run(); err != nil {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	fmt.Println("✓ SupaCraft service removed")
	return nil
}

func runServiceStatus() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd is only available on Linux")
	}

	c := exec.Command("systemctl", "status", serviceName)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	// systemctl status returns exit code 3 if inactive — don't treat as error
	_ = c.Run()
	return nil
}

// maybeRootCmd returns a Command that runs directly when already root,
// otherwise prepends sudo. This handles VPS environments where the user is
// root and sudo is not installed.
func maybeRootCmd(name string, args ...string) *exec.Cmd {
	if os.Getuid() == 0 {
		return exec.Command(name, args...)
	}
	return sudoCmd(name, args...)
}

func sudoCmd(name string, args ...string) *exec.Cmd {
	return exec.Command("sudo", append([]string{name}, args...)...)
}

func generateUnit(binPath, username, homeDir, workDir string) string {
	return fmt.Sprintf(`[Unit]
Description=SupaCraft API Server
After=network.target

[Service]
Type=simple
User=%s
WorkingDirectory=%s
ExecStart=%s serve --dir %s
Restart=on-failure
RestartSec=5
Environment=HOME=%s

[Install]
WantedBy=multi-user.target
`, username, workDir, binPath, workDir, homeDir)
}
