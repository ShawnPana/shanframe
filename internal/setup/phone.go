package setup

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/shawnpana/shanframe/internal/android"
)

// ensureAndroid makes this phone a screen target: the adb client present,
// and the shell-uid broker running (started once per boot through the
// phone's own Wireless debugging). The one step Android forces on the user
// — a pairing code — is reported in plain words until it's done
// (`shanframe pair <code>`); after a restart, a moment on an allowed Wi‑Fi.
func ensureAndroid() Screen {
	if !android.HasADB() {
		installADBOnce()
		return Screen{Note: "setting up screen sharing (a minute or two)"}
	}
	switch err := android.Ensure(); {
	case err == nil:
		return Screen{Ready: true}
	case errors.Is(err, android.ErrNotPaired):
		return Screen{Note: "to share this phone's screen: Settings → Developer options → Wireless debugging → Pair device with pairing code, then run: shanframe pair <code>"}
	case errors.Is(err, android.ErrNoListener):
		return Screen{Note: "to share this phone's screen, turn on Wireless debugging (Settings → Developer options) while on Wi‑Fi — needed once after each restart"}
	default:
		return Screen{Note: "screen sharing isn't available right now — " + err.Error()}
	}
}

var adbInstall sync.Once

func installADBOnce() {
	adbInstall.Do(func() {
		go func() {
			if err := android.InstallADB(); err != nil {
				log.Printf("setup: %v", err)
				return
			}
			log.Printf("setup: installed adb for screen sharing")
		}()
	})
}

// --- the agent as a background process on Android (Termux): no init system
// to speak of, so `up` starts it detached with a wake lock and writes a
// Termux:Boot script so it comes back after a restart.

func termuxHome() string {
	home, _ := os.UserHomeDir()
	return home
}

func bootScriptPath() string { return filepath.Join(termuxHome(), ".termux", "boot", "shanframe") }

func installAndroid(exe, logPath string) error {
	script := "#!/data/data/com.termux/files/usr/bin/sh\n" +
		"# shanframe: start the agent at boot (Termux:Boot runs this)\n" +
		"termux-wake-lock 2>/dev/null\n" +
		"exec " + exe + " serve >> " + logPath + " 2>&1\n"
	if err := os.MkdirAll(filepath.Dir(bootScriptPath()), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(bootScriptPath(), []byte(script), 0o700); err != nil {
		return err
	}
	stopAndroid()
	android.Command("termux-wake-lock").Run() // keeps the phone from freezing us; no-op elsewhere
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "serve")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// stopAndroid ends a running agent started by installAndroid (or by hand).
func stopAndroid() {
	out, _ := android.Command("pgrep", "-f", "shanframe serve").Output()
	for _, f := range strings.Fields(string(out)) {
		if f == "" {
			continue
		}
		android.Command("kill", f).Run()
	}
}

func uninstallAndroid() error {
	os.Remove(bootScriptPath())
	stopAndroid()
	return nil
}

// hasTermuxBoot reports whether the Termux:Boot add-on looks installed (it
// creates ~/.termux/boot on first launch).
func hasTermuxBoot() bool {
	st, err := os.Stat(filepath.Join(termuxHome(), ".termux", "boot"))
	return err == nil && st.IsDir()
}
