package setup

import (
	"github.com/shawnpana/shanframe/internal/input"
	"github.com/shawnpana/shanframe/internal/screencap"
)

// EnsureScreen readies the native screen target: capture (ScreenCaptureKit)
// and input (CGEvents). Both need a one-time macOS permission — Screen
// Recording and Accessibility — which the OS grants to this binary via its
// own prompts; we ask once and then report readiness in the device list.
// This replaced the Screen Sharing / VNC path (and its account-password
// prompt) with the native capture agent.
func EnsureScreen() Screen {
	if !screencap.Supported() {
		return Screen{Note: "this build has no screen capture"}
	}
	cap, in := screencap.Authorized(), input.Authorized()
	if cap && in {
		return Screen{Ready: true}
	}
	// ask for what's missing (each prompts at most once per grant state)
	if !cap {
		screencap.RequestPermission()
	}
	if !in {
		input.RequestPermission()
	}
	cap, in = screencap.Authorized(), input.Authorized()
	switch {
	case cap && in:
		return Screen{Ready: true}
	case !cap && !in:
		return Screen{Note: "allow Screen Recording and Accessibility for shanframe on this Mac (System Settings → Privacy & Security)"}
	case !cap:
		return Screen{Note: "allow Screen Recording for shanframe on this Mac (System Settings → Privacy & Security)"}
	default:
		return Screen{Ready: true, Note: "view only until Accessibility is allowed for shanframe on this Mac"}
	}
}
