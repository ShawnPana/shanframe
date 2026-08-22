package main

// The native screen service: capture as an H.264 video track, input events
// from the controller injected locally. This replaces VNC on platforms with
// native capture (macOS today); others keep the "vnc" service until they get
// their own (internal/screencap, internal/input).

import (
	"encoding/base64"
	"encoding/json"
	"hash/fnv"
	"io"
	"log"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/shawnpana/shanframe/internal/frame"
	"github.com/shawnpana/shanframe/internal/input"
	"github.com/shawnpana/shanframe/internal/screencap"
)

// nativeScreen reports whether this device serves the native screen path.
func nativeScreen() bool { return screencap.Supported() }

// attachScreen adds a video track to pc and starts feeding it captured
// frames of the display. Returns a stop func for session teardown.
func attachScreen(pc *webrtc.PeerConnection) (func(), error) {
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "screen", "shanframe")
	if err != nil {
		return nil, err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var lastPTS int64
	onFrame := func(f screencap.Frame) {
		mu.Lock()
		d := time.Duration(f.PTSMs-lastPTS) * time.Millisecond
		lastPTS = f.PTSMs
		mu.Unlock()
		if d <= 0 || d > 5*time.Second {
			d = 33 * time.Millisecond
		}
		track.WriteSample(media.Sample{Data: f.Data, Duration: d})
	}
	sess, err := screencap.Start(1920, 30, 4_000_000, onFrame)
	if err != nil {
		return nil, err
	}
	log.Printf("screen → native %dx%d", sess.W, sess.H)
	go func() { // viewers ask for a fresh keyframe after loss (PLI/FIR)
		buf := make([]byte, 1500)
		for {
			n, _, err := sender.Read(buf)
			if err != nil {
				return
			}
			pkts, err := rtcp.Unmarshal(buf[:n])
			if err != nil {
				continue
			}
			for _, p := range pkts {
				switch p.(type) {
				case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
					sess.ForceKeyframe()
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { sess.Stop(); log.Printf("screen ← native closed") }) }, nil
}

// screenEvent is one input message from the controller.
type screenEvent struct {
	T  string  `json:"t"`           // mv | btn | wheel | key | txt
	X  float64 `json:"x,omitempty"` // mv: normalized 0..1
	Y  float64 `json:"y,omitempty"`
	B  int     `json:"b"`            // btn: 0 left, 1 right, 2 middle
	D  bool    `json:"d"`            // btn/key: down
	DX float64 `json:"dx,omitempty"` // wheel: pixels
	DY float64 `json:"dy,omitempty"`
	K  string  `json:"k,omitempty"` // key: DOM key name
	S  string  `json:"s,omitempty"` // txt: string to type
}

// serveScreenInput reads input events off the service stream and injects
// them until the stream closes.
func serveScreenInput(s io.ReadWriter) error {
	if !input.Supported() || !input.Authorized() {
		// not fatal: view-only is still useful; the page shows the note
		frame.Write(s, frame.Data, []byte(`{"t":"noinput","note":"view only — allow Accessibility for shanframe on this Mac"}`))
	}
	inj := input.New()
	defer inj.ReleaseAll()
	dw, dh := input.DisplaySize()
	ready, _ := json.Marshal(map[string]any{"t": "ready", "w": dw, "h": dh})
	frame.Write(s, frame.Data, ready)
	done := make(chan struct{})
	defer close(done)
	go watchCursor(s, done)
	for {
		typ, p, err := frame.Read(s)
		if err != nil {
			return err
		}
		if typ != frame.Data {
			continue
		}
		var ev screenEvent
		if json.Unmarshal(p, &ev) != nil {
			continue
		}
		switch ev.T {
		case "mv":
			inj.Move(ev.X, ev.Y)
		case "btn":
			inj.Button(ev.B, ev.D)
		case "wheel":
			inj.Wheel(ev.DX, ev.DY)
		case "key":
			inj.Key(ev.K, ev.D)
		case "txt":
			inj.Text(ev.S)
		}
	}
}

// watchCursor streams cursor-shape changes to the viewer. The capture
// composites no cursor — a local sprite tracks input with zero latency, and
// these updates keep its shape honest (I-beam over text, resize arrows, …).
// Sole writer on s once the ready message is out.
func watchCursor(s io.Writer, done <-chan struct{}) {
	if _, _, _, _, _, ok := screencap.Cursor(); !ok {
		return
	}
	var last uint64
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		png, hx, hy, w, h, ok := screencap.Cursor()
		if !ok {
			continue
		}
		hash := fnv.New64a()
		hash.Write(png)
		if sum := hash.Sum64(); sum == last {
			continue
		} else {
			last = sum
		}
		b, _ := json.Marshal(map[string]any{"t": "cursor",
			"png": base64.StdEncoding.EncodeToString(png), "hx": hx, "hy": hy, "w": w, "h": h})
		if frame.Write(s, frame.Data, b) != nil {
			return
		}
	}
}
