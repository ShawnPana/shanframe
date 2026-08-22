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
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	OS       string   `json:"os"`
	Online   bool     `json:"online"`
	Asleep   bool     `json:"asleep,omitempty"`   // offline because the machine is sleeping; back on wake
	Screen   bool     `json:"screen"`             // desktop available
	Native   bool     `json:"native,omitempty"`   // desktop is native capture (H.264 track), not VNC
	Note     string   `json:"note,omitempty"`     // why not, in plain words
	Services []string `json:"services,omitempty"` // what the device offers right now: shell, exec, screen, input, …
	StartCmd string   `json:"startCmd,omitempty"` // account setting: typed into every new terminal on this device
	Auth     string   `json:"auth,omitempty"`     // "account" when the viewer signs in with the device's account
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
	To        string `json:"to,omitempty"`
	From      string `json:"from,omitempty"`
	Session   string `json:"session,omitempty"`
	SDP       string `json:"sdp,omitempty"`
	Candidate string `json:"candidate,omitempty"` // JSON of RTCIceCandidateInit
	Open      *Open  `json:"open,omitempty"`      // on offers: what the session wants (lets the agent attach media before answering)

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
type Open struct {
	Service string `json:"service"` // "shell" | "exec" | "tunnel" | "tcp" | "vnc" | "screen" | "info"
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
	Cmd     string `json:"cmd,omitempty"`  // exec: one command line, run by the device's login shell
	Host    string `json:"host,omitempty"` // tcp: dial this host (resolved on the device) …
	Port    int    `json:"port,omitempty"` // … and port; the stream is the raw TCP bytes after one status byte

	// screen service: capture one window instead of the display — the window
	// at these screen bounds (points), cropped below CropTop (the app's own
}
