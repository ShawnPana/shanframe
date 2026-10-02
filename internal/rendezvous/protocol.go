// Package rendezvous is the wire protocol between devices/clients and the
// shanframe server: presence and WebRTC signaling. Single WebSocket per
// participant, JSON messages, one owner token (single-tenant for now — every
// record still carries an account id so sign-up is a login page later).
package rendezvous

// Kinds of participant.
const (
	KindAgent  = "agent"  // a device offering shell/screen
	KindClient = "client" // a phone/desktop/CLI controlling devices
)

// Device is what the server knows about a registered device.
type Device struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	OS         string   `json:"os"`
	Online     bool     `json:"online"`
	Asleep     bool     `json:"asleep,omitempty"`     // offline because the machine is sleeping; back on wake
	Screen     bool     `json:"screen"`               // desktop available
	Native     bool     `json:"native,omitempty"`     // desktop is native capture (H.264 track), not VNC
	Note       string   `json:"note,omitempty"`       // why not, in plain words
	TargetOnly bool     `json:"targetOnly,omitempty"` // reachable, but its key can't control other devices
	Services   []string `json:"services,omitempty"`   // what the device offers right now: shell, exec, screen, input, …
	StartCmd   string   `json:"startCmd,omitempty"`   // account setting: typed into every new terminal on this device
	OSName     string   `json:"osName,omitempty"`     // pretty OS: "macOS 26.5", "Debian 12 (bookworm)"
	Arch       string   `json:"arch,omitempty"`       // arm64 / amd64
	Model      string   `json:"model,omitempty"`      // hardware: "MacBook Pro (Mac16,6)", "Raspberry Pi 5"
	Build      string   `json:"build,omitempty"`      // agent build (git sha)
	Auth       string   `json:"auth,omitempty"`       // "account" when the viewer signs in with the device's account
	// Grants: to a controller, what this one-way device is allowed to reach
	// (each names the target). To the one-way device itself, its list holds
	// only the devices it may reach, and each entry's Grants say which ports.
	Grants []Grant `json:"grants,omitempty"`
}

// Grant lets a one-way device reach exactly one TCP port on one device of
// the account, and nothing else: no shell, no names, no other devices. The
// server stamps the offer with the grants that apply (never taken from the
// caller), and the target device refuses every stream that isn't a tcp
// stream to a granted host:port.
type Grant struct {
	To   string `json:"to,omitempty"` // device id (omitted where the reader is that device)
	Host string `json:"host"`
	Port int    `json:"port"`
}

// Msg is every message on the socket. Type decides which fields matter.
type Msg struct {
	T string `json:"t"`

	// hello (both directions at open)
	Kind   string  `json:"kind,omitempty"`   // KindAgent | KindClient
	Device *Device `json:"device,omitempty"` // agent → server: who I am (+ readiness updates)
	Conn   string  `json:"conn,omitempty"`   // server → participant: your connection id

	// devices (server → clients, full list on connect and on any change)
	Devices []Device `json:"devices,omitempty"`

	// ice-servers (server → participant): what to hand RTCPeerConnection
	ICEServers []ICEServer `json:"iceServers,omitempty"`

	// signaling: offer | answer | ice — routed by To (a device id for
	// client→agent, a conn id for agent→client); server fills From.
	To        string  `json:"to,omitempty"`
	From      string  `json:"from,omitempty"`
	Session   string  `json:"session,omitempty"`
	SDP       string  `json:"sdp,omitempty"`
	Candidate string  `json:"candidate,omitempty"` // JSON of RTCIceCandidateInit
	Open      *Open   `json:"open,omitempty"`      // on offers: what the session wants (lets the agent attach media before answering)
	Caller    *Caller `json:"caller,omitempty"`    // on offers, server → agent: who is calling. Set by the server from the caller's key; never trusted from a client.
	Grants    []Grant `json:"grants,omitempty"`    // on offers, server → agent: the caller is a one-way device and may open only tcp streams to these. Server-set; never trusted from a client.

	// error (server → participant)
	Error string `json:"error,omitempty"`

	// report (agent → server): a crash from the previous run, for the operator
	Report string `json:"report,omitempty"`

	// set (client → server): per-device settings for the whole account, keyed
	// by name ("startCmd"); the server stores and rebroadcasts the list
	Set map[string]string `json:"set,omitempty"`
}

type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// DataChannel labels carry the service request as JSON.
// Caller is who opened a session, as the account's owner would recognise
// them: the key's label ("browser session 2026-08-23", "Studio Mac") and
// whether it is a signed-in browser/app or another of the account's machines.
type Caller struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // "controller" (a signed-in browser or app) | "agent" (another machine's CLI)
}

type Open struct {
	Service string `json:"service"` // "shell" | "exec" | "tunnel" | "tcp" | "vnc" | "screen" | "info"
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
	Cmd     string `json:"cmd,omitempty"`  // exec: one command line, run by the device's login shell
	Priv    bool   `json:"priv,omitempty"` // exec: as the device's privileged helper (Android: the shell user)
	Host    string `json:"host,omitempty"` // tcp: dial this host (resolved on the device) …
	Port    int    `json:"port,omitempty"` // … and port; the stream is the raw TCP bytes after one status byte

	// screen and screenshot: which display, numbered as the device numbers
	// them (main = 1, then left to right; the screen stream's ready message
	// lists them). 0 is the main display.
	Display int `json:"display,omitempty"`
}
