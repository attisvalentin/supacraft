package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/attisvalentin/supacraft/internal/service"
	"github.com/spf13/cobra"
)

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage the SupaCraft systemd service",
	Long:  "Install, uninstall, or check the status of the SupaCraft systemd service on Linux.",
}

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install and start the SupaCraft systemd service",
	RunE: func(cmd *cobra.Command, args []string) error {
		workDir, flag, err := serviceTarget(serviceDataDir)
		if err != nil {
			return err
		}
		if err := service.Install(service.Options{Out: os.Stdout}, workDir, flag); err != nil {
			return err
		}
		fmt.Println("\n✓ SupaCraft service installed and started")
		fmt.Println("\nUseful commands:")
		fmt.Println("  sudo systemctl status supacraft    — check status")
		fmt.Println("  sudo systemctl restart supacraft   — restart")
		fmt.Println("  sudo systemctl stop supacraft      — stop")
		fmt.Println("  sudo journalctl -u supacraft -f    — follow logs")
		return nil
	},
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop and remove the SupaCraft systemd service",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := service.Uninstall(service.Options{Out: os.Stdout}); err != nil {
			return err
		}
		fmt.Println("✓ SupaCraft service removed")
		return nil
	},
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the status of the SupaCraft systemd service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return service.Status(service.Options{Out: os.Stdout})
	},
}

var serviceDataDir string

func init() {
	serviceInstallCmd.Flags().StringVar(&serviceDataDir, "data-dir", "", "run the service for every tenant in this central data directory")
	serviceCmd.AddCommand(serviceInstallCmd)
	serviceCmd.AddCommand(serviceUninstallCmd)
	serviceCmd.AddCommand(serviceStatusCmd)
	rootCmd.AddCommand(serviceCmd)
}

// serviceTarget is what the service serves: dataDir, else the project.
func serviceTarget(dataDir string) (workDir, flag string, err error) {
	if dataDir != "" {
		workDir, err = filepath.Abs(dataDir)
		return workDir, "--data-dir", err
	}
	p, err := loadProject()
	if err != nil {
		return "", "", err
	}
	return p.Dir, "--dir", nil
}
