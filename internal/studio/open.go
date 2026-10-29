package studio

import (
	"os/exec"
	"runtime"
)

// OpenBrowser best-effort opens url in the user's default browser. Errors are
// returned so the caller can fall back to printing the URL.
func OpenBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{url}
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		cmd, args = "xdg-open", []string{url}
	}
	return exec.Command(cmd, args...).Start()
}
