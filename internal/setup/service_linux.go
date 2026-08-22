package setup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
)

// Linux service install picks the best supervisor that's actually available,
// in this order:
//   1. systemd + root/passwordless sudo  → system unit (boots with the machine)
//   2. systemd, no root                  → user unit (+ linger so it survives logout)
//   3. no systemd                        → tell the user exactly what to do
// Never silently degrade: the install output names the mode chosen.

const systemUnitPath = "/etc/systemd/system/shanframe.service"

func userUnitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", "shanframe.service")
}

func haveSystemd() bool {
	st, err := os.Stat("/run/systemd/system")
	return err == nil && st.IsDir()
}

func haveRoot() bool {
	if os.Geteuid() == 0 {
		return true
	}
	return exec.Command("sudo", "-n", "true").Run() == nil
}

// installedMode reports which kind of unit is present: "system", "user" or "".
func installedMode() string {
	if _, err := os.Stat(systemUnitPath); err == nil {
		return "system"
	}
	if _, err := os.Stat(userUnitPath()); err == nil {
		return "user"
	}
	return ""
}

func InstallService(exe, logPath string) error {
	if !haveSystemd() {
		return errors.New("no systemd on this machine — run `shanframe serve` under tmux/screen, or add it to your init system; it keeps itself connected and updated")
	}
	u, err := user.Current()
	if err != nil {
		return err
	}
	if haveRoot() {
		unit := fmt.Sprintf(`[Unit]
Description=shanframe agent
After=network-online.target
Wants=network-online.target

[Service]
User=%s
Environment=HOME=%s
ExecStart=%s serve
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
`, u.Username, u.HomeDir, exe)
		if err := writeAsRoot(systemUnitPath, unit); err != nil {
			return err
		}
		if err := runAsRoot("systemctl", "daemon-reload"); err != nil {
			return err
		}
		return runAsRoot("systemctl", "--no-block", "enable", "--now", "shanframe")
	}
	// user unit: no root needed; linger keeps it alive when you log out
	unit := fmt.Sprintf(`[Unit]
Description=shanframe agent
After=network-online.target

[Service]
ExecStart=%s serve
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
`, exe)
	p := userUnitPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(unit), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user: %v: %s", err, out)
	}
	if out, err := exec.Command("systemctl", "--user", "--no-block", "enable", "--now", "shanframe").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user enable: %v: %s", err, out)
	}
	exec.Command("loginctl", "enable-linger", u.Username).Run() // best effort; without it the agent runs while you're logged in
	return nil
}

func LogHint(logPath string) string {
	if installedMode() == "user" {
		return "journalctl --user -u shanframe -f"
	}
	return "journalctl -u shanframe -f"
}

func UninstallService() error {
	switch installedMode() {
	case "user":
		exec.Command("systemctl", "--user", "disable", "--now", "shanframe").Run()
		os.Remove(userUnitPath())
		exec.Command("systemctl", "--user", "daemon-reload").Run()
		return nil
	default:
		runAsRoot("systemctl", "disable", "--now", "shanframe")
		if err := runAsRoot("rm", "-f", systemUnitPath); err != nil {
			return err
		}
		return runAsRoot("systemctl", "daemon-reload")
	}
}

func ServiceDescription() string {
	if installedMode() == "user" {
		return "systemd user unit shanframe.service (no root; lingers across logout where allowed)"
	}
	return "systemd unit shanframe.service"
}

// RestartService relaunches the agent so config changes take effect.
func RestartService() error {
	if installedMode() == "user" {
		return exec.Command("systemctl", "--user", "--no-block", "restart", "shanframe").Run()
	}
	return runAsRoot("systemctl", "--no-block", "restart", "shanframe")
}
