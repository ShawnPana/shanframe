package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const label = "com.shanframe.daemon"

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

// InstallService registers `shanframe serve` as a per-user launchd agent that
// starts at login and is kept alive. The agent runs as this user, so its
// binary must live where this user can write, or it can never update itself
// (install.sh puts it in root-owned /usr/local/bin; a Mac sat on a stale
// build for a week that way).
func InstallService(exe, logPath string) error {
	owned, moved, err := OwnBinary(exe, filepath.Dir(logPath))
	if err != nil {
		return err
	}
	if moved {
		fmt.Printf("agent binary: %s (a place it can update itself)\n", owned)
		// keep the PATH name current when we can do it without asking
		if exec.Command("sudo", "-n", "true").Run() == nil && exec.Command("sudo", "-n", "ln", "-sfn", owned, exe).Run() == nil {
			fmt.Printf("  %s now points there, so the command stays current\n", exe)
		} else {
			fmt.Printf("  to keep `shanframe` on your PATH current: sudo ln -sfn %s %s\n", owned, exe)
		}
		exe = owned
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>serve</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin</string></dict>
</dict></plist>
`, label, exe, logPath, logPath)
	if err := os.MkdirAll(filepath.Dir(plistPath()), 0o755); err != nil {
		return err
	}
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	// Replace if present: bootout is asynchronous — wait until the job is
	// really gone, or the bootstrap that follows fails with exit 5 (EIO).
	exec.Command("launchctl", "bootout", target).Run()
	for i := 0; i < 50; i++ {
		if exec.Command("launchctl", "print", target).Run() != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := os.WriteFile(plistPath(), []byte(plist), 0o644); err != nil {
		return err
	}
	var out []byte
	for i := 0; i < 5; i++ {
		out, err = exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plistPath()).CombinedOutput()
		if err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
}

func UninstallService() error {
	exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Run()
	return os.Remove(plistPath())
}

func ServiceDescription() string { return "launchd agent " + label + " (" + plistPath() + ")" }

// LogHint says where the service writes its log.
func LogHint(logPath string) string { return logPath }

// RestartService relaunches the agent so config changes take effect.
func RestartService() error {
	return exec.Command("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Run()
}

// ServiceInstalled reports whether a service manager runs the agent here.
func ServiceInstalled() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}
