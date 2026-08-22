// Package-side machine facts: what the device can say about itself beyond
// runtime.GOOS. Collected once at agent start — cheap probes only.
package setup

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// OSPretty is the human name of the installed OS: "macOS 26.5",
// "Debian GNU/Linux 12 (bookworm)". Falls back to the platform name.
func OSPretty() string {
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				return "macOS " + v
			}
		}
	case "linux":
		if b, err := os.ReadFile("/etc/os-release"); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
					return strings.Trim(v, `"`)
				}
			}
		}
	}
	return runtime.GOOS
}

// Model is the hardware: "MacBook Pro (Mac16,6)", "Raspberry Pi 5 Model B".
func Model() string {
	switch runtime.GOOS {
	case "darwin":
		name, id := "", ""
		if out, err := exec.Command("sysctl", "-n", "hw.model").Output(); err == nil {
			id = strings.TrimSpace(string(out))
		}
		if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil {
			_ = out // computer name is the device name already; prefer the family below
		}
		if out, err := exec.Command("sysctl", "-n", "hw.product").Output(); err == nil {
			name = strings.TrimSpace(string(out))
		}
		switch {
		case name != "" && id != "" && name != id:
			return name + " (" + id + ")"
		case id != "":
			return id
		}
	case "linux":
		if b, err := os.ReadFile("/sys/firmware/devicetree/base/model"); err == nil {
			return strings.TrimRight(string(b), "\x00\n ")
		}
		if b, err := os.ReadFile("/sys/class/dmi/id/product_name"); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}
