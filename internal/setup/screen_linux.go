package setup

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// cmdTimeout bounds every privileged command: a wedged systemctl on an
// overloaded box must not stall the daemon's readiness loop.
const cmdTimeout = 30 * time.Second

const wayvncConf = "/etc/wayvnc/config"

// wantWayvnc is the whole config: loopback only, no VNC-level auth. TLS and
// passwords are redundant inside the mesh and noVNC can't speak wayvnc's
// RSA-AES / VeNCrypt-X509 anyway.
const wantWayvnc = "use_relative_paths=true\naddress=127.0.0.1\nenable_auth=false\n"

// EnsureScreen makes wayvnc (Raspberry Pi OS / wlroots desktops) usable as a
// shanframe screen target. Idempotent; safe to call on every start.
func EnsureScreen() Screen {
	if _, err := os.Stat(wayvncConf); err != nil {
		if vncListening() {
			return Screen{Ready: true}
		}
		if !hasDesktop() {
			return Screen{} // a headless box is terminal-only by nature; nothing to nag about
		}
		return Screen{Note: "desktop sharing isn't available on this Linux desktop yet — terminal works"}
	}
	cur, _ := os.ReadFile(wayvncConf)
	if string(cur) != wantWayvnc {
		if err := writeAsRoot(wayvncConf, wantWayvnc); err != nil {
			log.Printf("setup: wayvnc config: %v", err)
			return Screen{Note: "screen server needs an admin password on this device"}
		}
		if err := runAsRoot("systemctl", "--no-block", "restart", "wayvnc"); err != nil {
			log.Printf("setup: restart wayvnc: %v", err)
		}
		log.Printf("setup: configured wayvnc for shanframe (loopback, mesh-authenticated)")
	}
	if !vncListening() {
		runAsRoot("systemctl", "--no-block", "enable", "--now", "wayvnc")
	}
	if vncListening() {
		return Screen{Ready: true}
	}
	return Screen{Note: "screen server isn't running (no desktop session?)"}
}

// hasDesktop reports whether anyone could see a screen here at all: a
// Wayland or X11 session for some user, or a display manager running.
func hasDesktop() bool {
	if os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != "" {
		return true
	}
	if m, _ := filepath.Glob("/run/user/*/wayland-*"); len(m) > 0 {
		return true
	}
	if m, _ := filepath.Glob("/tmp/.X11-unix/X*"); len(m) > 0 {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "display-manager").Run() == nil
}

func writeAsRoot(path, content string) error {
	if os.Geteuid() == 0 {
		return os.WriteFile(path, []byte(content), 0o644)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sudo", "-n", "tee", path)
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sudo tee %s: %v: %s", path, err, out)
	}
	return nil
}

func runAsRoot(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.CommandContext(ctx, name, args...)
	} else {
		cmd = exec.CommandContext(ctx, "sudo", append([]string{"-n", name}, args...)...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %v: %v: %s", name, args, err, out)
	}
	return nil
}
