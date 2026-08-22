// Package peer is the WebRTC layer: one PeerConnection per session, one
// DataChannel per service stream, carrying the frame protocol. Pion on
// devices and the CLI; the browser uses RTCPeerConnection with the same
// labels and framing (see webui/static/sf.js).
package peer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/shawnpana/shanframe/internal/rendezvous"
)

func api() *webrtc.API {
	se := webrtc.SettingEngine{}
	se.DetachDataChannels() // gives us io.ReadWriteCloser per channel
	// Real network interfaces only. VPN/tunnel/container interfaces (Tailscale
	// utun*, docker bridges, …) otherwise become ICE candidates, and a session
	// nominated onto one rides a relay detour that stutters and dies — screen
	// sessions to the Mac froze every few seconds while Tailscale was up.
	se.SetInterfaceFilter(func(name string) bool {
		for _, bad := range []string{"utun", "tun", "tap", "tailscale", "docker", "br-", "veth", "awdl", "llw", "zt"} {
			if strings.HasPrefix(name, bad) {
				return false
			}
		}
		return true
	})
	return webrtc.NewAPI(webrtc.WithSettingEngine(se))
}

func config(ice []rendezvous.ICEServer) webrtc.Configuration {
	var servers []webrtc.ICEServer
	for _, s := range ice {
		servers = append(servers, webrtc.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	return webrtc.Configuration{ICEServers: servers}
}

// stream adapts a detached DataChannel (message-oriented) to a byte stream
// that frame.Read can consume with partial reads.
type stream struct {
	dc  io.ReadWriteCloser
	buf []byte
	tmp []byte
}

func newStream(dc io.ReadWriteCloser) *stream { return &stream{dc: dc, tmp: make([]byte, 2<<20)} }

func (s *stream) Read(p []byte) (int, error) {
	if len(s.buf) == 0 {
		n, err := s.dc.Read(s.tmp)
		if err != nil {
			return 0, err
		}
		s.buf = append(s.buf[:0], s.tmp[:n]...)
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}
func (s *stream) Write(p []byte) (int, error) { return s.dc.Write(p) }
func (s *stream) Close() error                { return s.dc.Close() }

// Conn is one WebRTC session with a remote participant.
type Conn struct {
	pc      *webrtc.PeerConnection
	mu      sync.Mutex
	pending []webrtc.ICECandidateInit
	remote  bool
}

// Handler receives each service stream a remote opened on us.
type Handler func(open rendezvous.Open, s io.ReadWriteCloser)

// Answer handles an incoming offer: builds the PeerConnection, wires
// DataChannels to h, returns the answer SDP. Local ICE candidates go to
// onCandidate (as RTCIceCandidateInit JSON) to be relayed to the offerer.
// If the offer asks for video and `video` is non-nil, it is called with the
// PeerConnection before the answer so a track can be attached (the native
// screen); it must clean itself up via onClosed.
func Answer(ice []rendezvous.ICEServer, offerSDP string, h Handler, video func(pc *webrtc.PeerConnection) error, onCandidate func(string), onClosed func()) (*Conn, string, error) {
	pc, err := api().NewPeerConnection(config(ice))
	if err != nil {
		return nil, "", err
	}
	if video != nil && strings.Contains(offerSDP, "m=video") {
		if err := video(pc); err != nil {
			pc.Close()
			return nil, "", err
		}
	}
	c := &Conn{pc: pc}
	pc.OnICECandidate(func(cand *webrtc.ICECandidate) {
		if cand == nil {
			return
		}
		b, _ := json.Marshal(cand.ToJSON())
		onCandidate(string(b))
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		if st == webrtc.PeerConnectionStateFailed || st == webrtc.PeerConnectionStateClosed || st == webrtc.PeerConnectionStateDisconnected {
			if onClosed != nil {
				onClosed()
			}
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		var open rendezvous.Open
		if json.Unmarshal([]byte(dc.Label()), &open) != nil {
			dc.Close()
			return
		}
		dc.OnOpen(func() {
			raw, err := dc.Detach()
			if err != nil {
				return
			}
			go h(open, newStream(raw))
		})
	})
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		pc.Close()
		return nil, "", err
	}
	c.mu.Lock()
	c.remote = true
	c.mu.Unlock()
	ans, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return nil, "", err
	}
	if err := pc.SetLocalDescription(ans); err != nil {
		pc.Close()
		return nil, "", err
	}
	return c, ans.SDP, nil
}

// Offer starts a session toward a device and opens one service stream. The
// returned Conn needs SetAnswer + AddCandidate as signaling arrives; the
// stream is delivered on ready once the channel opens.
func Offer(ice []rendezvous.ICEServer, open rendezvous.Open, onCandidate func(string), ready func(io.ReadWriteCloser), onClosed func()) (*Conn, string, error) {
	pc, err := api().NewPeerConnection(config(ice))
	if err != nil {
		return nil, "", err
	}
	c := &Conn{pc: pc}
	pc.OnICECandidate(func(cand *webrtc.ICECandidate) {
		if cand == nil {
			return
		}
		b, _ := json.Marshal(cand.ToJSON())
		onCandidate(string(b))
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		if st == webrtc.PeerConnectionStateFailed || st == webrtc.PeerConnectionStateClosed || st == webrtc.PeerConnectionStateDisconnected {
			if onClosed != nil {
				onClosed()
			}
		}
	})
	label, _ := json.Marshal(open)
	dc, err := pc.CreateDataChannel(string(label), nil)
	if err != nil {
		pc.Close()
		return nil, "", err
	}
	dc.OnOpen(func() {
		raw, err := dc.Detach()
		if err != nil {
			return
		}
		ready(newStream(raw))
	})
	off, err := pc.CreateOffer(nil)
	if err != nil {
		pc.Close()
		return nil, "", err
	}
	if err := pc.SetLocalDescription(off); err != nil {
		pc.Close()
		return nil, "", err
	}
	return c, off.SDP, nil
}

// Dial opens one more service stream on an established session (a new
// DataChannel; no renegotiation needed). Used for tunnels: one stream per
// TCP connection.
func (c *Conn) Dial(open rendezvous.Open, timeout time.Duration) (io.ReadWriteCloser, error) {
	label, _ := json.Marshal(open)
	dc, err := c.pc.CreateDataChannel(string(label), nil)
	if err != nil {
		return nil, err
	}
	ready := make(chan io.ReadWriteCloser, 1)
	dc.OnOpen(func() {
		raw, err := dc.Detach()
		if err != nil {
			dc.Close()
			return
		}
		ready <- newStream(raw)
	})
	select {
	case s := <-ready:
		return s, nil
	case <-time.After(timeout):
		dc.Close()
		return nil, errors.New("stream open timed out")
	}
}

// SetAnswer completes the offerer's handshake.
func (c *Conn) SetAnswer(sdp string) error {
	if err := c.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}); err != nil {
		return err
	}
	c.mu.Lock()
	c.remote = true
	pend := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, p := range pend {
		c.pc.AddICECandidate(p)
	}
	return nil
}

// AddCandidate feeds a remote ICE candidate (RTCIceCandidateInit JSON).
func (c *Conn) AddCandidate(candJSON string) error {
	var init webrtc.ICECandidateInit
	if err := json.Unmarshal([]byte(candJSON), &init); err != nil {
		return err
	}
	c.mu.Lock()
	if !c.remote {
		c.pending = append(c.pending, init)
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	return c.pc.AddICECandidate(init)
}

// Close tears the session down.
func (c *Conn) Close() error { return c.pc.Close() }

// WaitConnected blocks until the connection is up or times out.
func (c *Conn) WaitConnected(d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		switch c.pc.ConnectionState() {
		case webrtc.PeerConnectionStateConnected:
			return nil
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			return errors.New("connection failed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timed out connecting")
}
