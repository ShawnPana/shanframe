// Package setup makes a machine a working shanframe target without the user
// doing anything: it configures/starts the local screen server, installs the
// daemon as a service, and reports readiness. Manual steps are defects.
package setup

import (
	"net"
	"time"
)

// VNCAddr is where a target's own VNC server must listen. Loopback only: the
// mesh identity gate is the auth, the VNC server is an implementation detail.
const VNCAddr = "127.0.0.1:5900"

// Screen describes whether this machine can be a desktop target.
type Screen struct {
	Ready bool   `json:"ready"`
	Note  string `json:"note,omitempty"` // plain words for the device list when not ready
	Auth  string `json:"auth,omitempty"` // "" (none) or "account" (viewer must sign in with the machine's account)
}

func vncListening() bool {
	c, err := net.DialTimeout("tcp", VNCAddr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}
