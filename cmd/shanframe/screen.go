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

// attachScreen adds a video track to pc. Capture starts when the controller
// opens its `screen` stream — that is where it names the display — so the
// track is silent until start runs. stop tears the capture down (fine before
// start, fine twice).
func attachScreen(pc *webrtc.PeerConnection) (start func(screencap.Display) error, stop func(), err error) {
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "screen", "shanframe")
	if err != nil {
		return nil, nil, err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		return nil, nil, err
	}
	var mu sync.Mutex
	var lastPTS int64
	var sess *screencap.Session
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
	var current int
	// start begins capture of a display; called again, it moves the capture
	// to another display on the same track (the viewer's video just changes
	// size), which is how a pointer dragged across an edge keeps its window
	start = func(d screencap.Display) error {
		mu.Lock()
		defer mu.Unlock()
		if sess != nil && current == d.N {
			return nil
		}
		if sess != nil {
			sess.Stop()
			sess = nil
		}
		s, err := screencap.Start(d.N, 1920, 30, 4_000_000, onFrame)
		if err != nil {
			return err
		}
		sess, current = s, d.N
		log.Printf("screen → native %dx%d (display %d, %s)", s.W, s.H, d.N, d.Name)
		return nil
	}
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
					mu.Lock()
					s := sess
					mu.Unlock()
					if s != nil {
						s.ForceKeyframe()
					}
				}
			}
		}
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			mu.Lock()
			s := sess
			mu.Unlock()
			if s != nil {
				s.Stop()
				log.Printf("screen ← native closed")
			}
		})
	}
	return start, stop, nil
}

// screenEvent is one input message from the controller.
type screenEvent struct {
	T  string  `json:"t"`           // mv | btn | wheel | key | txt | display
	N  int     `json:"n,omitempty"` // display: switch to display N (main = 1)
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
// them into display d until the stream closes. The ready message tells the
// controller the display's size, which display it got, and what else there
// is to switch to.
//
// switchTo, when given, moves the capture to another display mid-session:
// the pointer crossing an edge onto the display next door (a window being
// dragged across, say) takes the picture with it, and a "display" message
// from the viewer switches on request. Each switch is announced with a new
// ready message that also says where the pointer is.
func serveScreenInput(s io.ReadWriter, d screencap.Display, displays []screencap.Display, switchTo func(screencap.Display) error) error {
	var wmu sync.Mutex // the ready messages and the cursor watcher share s
	write := func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return frame.Write(s, frame.Data, b)
	}
	if !input.Supported() || !input.Authorized() {
		// not fatal: view-only is still useful; the page shows the note
		note, _ := json.Marshal(map[string]string{"t": "noinput", "note": input.Note()})
		write(note)
	}
	inj := input.New(input.Rect{X: d.X, Y: d.Y, W: d.W, H: d.H})
	defer inj.ReleaseAll()
	ready := func() {
		cx, cy := inj.Pos()
		b, _ := json.Marshal(map[string]any{"t": "ready", "w": d.W, "h": d.H, "display": d.N, "displays": displays, "cx": cx, "cy": cy})
		write(b)
	}
	ready()
	done := make(chan struct{})
	defer close(done)
	go watchCursor(s, &wmu, done)
	switchDisplay := func(to screencap.Display) {
		if switchTo == nil || to.N == d.N {
			return
		}
		if err := switchTo(to); err != nil {
			log.Printf("screen: display %d: %v", to.N, err)
			return
		}
		d = to
		inj.SetDisplay(input.Rect{X: d.X, Y: d.Y, W: d.W, H: d.H})
		ready()
	}
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
			// across an edge: the pointer is on another display now — show that one
			if ev.X < 0 || ev.X > 1 || ev.Y < 0 || ev.Y > 1 {
				cx, cy := inj.Pos()
				if to, ok := screencap.At(displays, cx, cy); ok && to.N != d.N {
					switchDisplay(to)
				}
			}
		case "display":
			if to, err := screencap.Pick(displays, ev.N); err == nil {
				switchDisplay(to)
			}
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
func watchCursor(s io.Writer, wmu *sync.Mutex, done <-chan struct{}) {
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
		wmu.Lock()
		err := frame.Write(s, frame.Data, b)
		wmu.Unlock()
		if err != nil {
			return
		}
	}
}
