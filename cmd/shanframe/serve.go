package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/shawnpana/shanframe/internal/frame"
	"github.com/shawnpana/shanframe/internal/peer"
	"github.com/shawnpana/shanframe/internal/power"
	"github.com/shawnpana/shanframe/internal/ptyx"
	"github.com/shawnpana/shanframe/internal/rendezvous"
	"github.com/shawnpana/shanframe/internal/screencap"
	"github.com/shawnpana/shanframe/internal/setup"
	"github.com/shawnpana/shanframe/internal/update"
)

// agent is this device as a target: connected to the server, answering
// sessions, serving shell/screen/info over WebRTC streams.
type agent struct {
	cfg Config
	rz  *rendezvous.Client

	mu       sync.Mutex
	screen   setup.Screen
	asleep   bool   // macOS told us sleep is imminent
	startCmd string // account setting, pushed by the server: typed into every new shell
	ice      []rendezvous.ICEServer
	conns    map[string]*peer.Conn // session → connection
	stops    map[string]func()     // session → screen-capture teardown
}

func serve() error {
	update.Guard(30 * time.Second) // confirm this build alive, or roll back
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	a := &agent{cfg: cfg, conns: map[string]*peer.Conn{}, stops: map[string]func(){}}
	a.rz = &rendezvous.Client{URL: cfg.wsURL(), Token: cfg.Token, OnMsg: a.onMsg}
	crash := lastCrash() // read before this run writes its own first line
	a.rz.OnConnect = func() {
		log.Printf("connected to %s as %q", cfg.Server, cfg.Name)
		if crash != "" {
			a.rz.Send(rendezvous.Msg{T: "report", Kind: build, Report: crash})
			log.Printf("reported the previous run's crash")
			crash = ""
		}
	}
	a.rz.OnUnauthorized = func() {
		log.Printf("%v", rendezvous.ErrUnauthorized)
		log.Printf("will check again in 10 minutes")
		time.Sleep(10 * time.Minute)
		update.Restart()
	}

	a.ensureReady()
	a.rz.Hello = a.hello()
	if power.Watch(func(asleep bool) {
		a.mu.Lock()
		a.asleep = asleep
		a.mu.Unlock()
		if asleep {
			log.Printf("going to sleep")
		} else {
			log.Printf("woke up")
		}
		a.rz.Send(rendezvous.Msg{T: "device", Device: a.device()}) // before the socket dies
	}) {
		log.Printf("watching sleep/wake")
	}
	go func() {
		waiting := 0 // minutes spent waiting on a permission grant
		for range time.Tick(time.Minute) {
			if a.ensureReady() {
				a.rz.Send(rendezvous.Msg{T: "device", Device: a.device()})
			}
			// macOS only applies a Screen Recording grant at process start, so
			// while unauthorized, re-exec every couple of minutes: the moment
			// the user grants, readiness follows without a manual restart.
			a.mu.Lock()
			ready := a.screen.Ready
			a.mu.Unlock()
			if !ready && nativeScreen() && !a.busy() {
				if waiting++; waiting >= 2 {
					log.Printf("restarting to pick up permission changes")
					update.Restart()
				}
			} else {
				waiting = 0
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		update.Confirm() // asked to stop ≠ crashed
		cancel()
	}()
	keepTunnels()
	go update.Loop(cfg.Server, cfg.Token, "shanframe", 5*time.Minute, a.busy)
	log.Printf("shanframe %q (build %s) → %s", cfg.Name, build, cfg.Server)
	a.rz.Run(ctx)
	return nil
}

// lastCrash returns the previous run's panic (its log lines after that run's
// start line) when it died on one, else "". The service supervisor restarts
// us after a crash; this is how the operator finds out it happened.
func lastCrash() string {
	lines := strings.Split(setup.RecentLog(filepath.Join(configDir(), "serve.log"), 300), "\n")
	start := 0
	for i, l := range lines {
		if strings.Contains(l, "shanframe \"") && strings.Contains(l, "(build ") {
			start = i
		}
	}
	seg := lines[start:]
	for _, l := range seg {
		if strings.HasPrefix(l, "panic:") || strings.HasPrefix(l, "fatal error:") || strings.Contains(l, "[signal SIG") {
			return strings.Join(seg, "\n")
		}
	}
	return ""
}

func (a *agent) hello() rendezvous.Msg {
	return rendezvous.Msg{T: "hello", Kind: rendezvous.KindAgent, Device: a.device()}
}

func (a *agent) device() *rendezvous.Device {
	a.mu.Lock()
	defer a.mu.Unlock()
	services := []string{"shell", "exec", "tunnel"}
	if a.screen.Ready {
		services = append(services, "screen", "input")
		if nativeScreen() {
			services = append(services, "screenshot")
		}
	}
	return &rendezvous.Device{ID: a.cfg.DeviceID, Name: a.cfg.Name, OS: osName(),
		Screen: a.screen.Ready, Native: nativeScreen(),
		Note: a.screen.Note, Auth: a.screen.Auth, Asleep: a.asleep, Services: services}
}

// ensureReady makes this machine a working target and reports whether the
// readiness changed. Runs at start and every minute: the app owns its setup.
func (a *agent) ensureReady() bool {
	scr := setup.EnsureScreen()
	a.mu.Lock()
	changed := scr != a.screen
	a.screen = scr
	a.mu.Unlock()
	if changed {
		if scr.Ready {
			log.Printf("screen target: ready")
		} else {
			log.Printf("screen target: not ready — %s", scr.Note)
		}
	}
	return changed
}

func (a *agent) onMsg(m rendezvous.Msg) {
	switch m.T {
	case "error":
		log.Printf("server: %s", m.Error)
	case "hello":
		a.mu.Lock()
		a.ice = m.ICEServers
		a.mu.Unlock()
	case "set": // per-device settings the server pushes (on connect and on change)
		if v, ok := m.Set["startCmd"]; ok {
			a.mu.Lock()
			a.startCmd = v
			a.mu.Unlock()
		}
	case "offer":
		a.mu.Lock()
		ice := a.ice
		a.mu.Unlock()
		from, session := m.From, m.Session
		var video func(*webrtc.PeerConnection) error
		if nativeScreen() {
			video = func(pc *webrtc.PeerConnection) error {
				stop, err := attachScreen(pc)
				if err != nil {
					return err
				}
				a.mu.Lock()
				a.stops[session] = stop
				a.mu.Unlock()
				return nil
			}
		}
		conn, answer, err := peer.Answer(ice, m.SDP, a.handleStream, video,
			func(cand string) { a.rz.Send(rendezvous.Msg{T: "ice", To: from, Session: session, Candidate: cand}) },
			func() { a.closeSession(session) })
		if err != nil {
			log.Printf("session %s: %v", session, err)
			return
		}
		a.mu.Lock()
		a.conns[session] = conn
		a.mu.Unlock()
		a.rz.Send(rendezvous.Msg{T: "answer", To: from, Session: session, SDP: answer})
	case "ice":
		a.mu.Lock()
		conn := a.conns[m.Session]
		a.mu.Unlock()
		if conn != nil {
			conn.AddCandidate(m.Candidate)
		}
	}
}

// busy reports whether any session is live (an update would cut it).
func (a *agent) busy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.conns) > 0 || tunnelsBusy()
}

func (a *agent) closeSession(session string) {
	a.mu.Lock()
	conn := a.conns[session]
	stop := a.stops[session]
	delete(a.conns, session)
	delete(a.stops, session)
	a.mu.Unlock()
	if stop != nil {
		stop()
	}
	if conn != nil {
		conn.Close()
	}
}

// handleStream serves one service over one DataChannel stream.
func (a *agent) handleStream(open rendezvous.Open, s io.ReadWriteCloser) {
	defer s.Close()
	switch open.Service {
	case "info":
		b, _ := json.Marshal(map[string]any{"screen": a.device()})
		frame.Write(s, frame.Data, b)
		return
	case "exec":
		serveExec(s, open.Cmd)
	case "tunnel": // control stream: stays open for the life of a tunnel session
		log.Printf("tunnel →")
		io.Copy(io.Discard, s)
		log.Printf("tunnel ← closed")
	case "tcp":
		serveTCP(s, open.Host, open.Port)
	case "shell":
		a.mu.Lock()
		start := a.startCmd
		a.mu.Unlock()
		log.Printf("shell → (%dx%d)", open.Cols, open.Rows)
		if err := ptyx.Serve(s, open.Cols, open.Rows, start); err != nil && !strings.Contains(err.Error(), "signal: killed") {
			log.Printf("shell ended: %v", err)
		}
		log.Printf("shell ← closed")
	case "screenshot": // one PNG: JSON header frame {w,h}, Data chunks, Exit
		png, w, h, err := screencap.Still()
		if err != nil {
			frame.Write(s, frame.Error, []byte(err.Error()))
			return
		}
		hdr, _ := json.Marshal(map[string]int{"w": w, "h": h, "bytes": len(png)})
		frame.Write(s, frame.Data, hdr)
		for len(png) > 0 {
			n := min(len(png), 32<<10)
			if frame.Write(s, frame.Data, png[:n]) != nil {
				return
			}
			png = png[n:]
		}
		frame.Write(s, frame.Exit, []byte{0, 0, 0, 0})
	case "screen":
		if err := serveScreenInput(s); err != nil && err != io.EOF && !strings.Contains(err.Error(), "abort chunk") {
			log.Printf("screen input ended: %v", err)
		}
	case "vnc":
		if nativeScreen() { // macOS dropped its VNC path with native capture
			frame.Write(s, frame.Error, []byte("this device uses the native screen — update your app"))
			return
		}
		log.Printf("screen →")
		if err := serveVNC(s); err != nil {
			log.Printf("screen ended: %v", err)
		}
		log.Printf("screen ← closed")
	default:
		frame.Write(s, frame.Error, []byte(fmt.Sprintf("unknown service %q", open.Service)))
	}
}

// serveVNC bridges the machine's own VNC server (RFB bytes) to Data frames.
func serveVNC(rw io.ReadWriter) error {
	v, err := net.DialTimeout("tcp", setup.VNCAddr, 5*time.Second)
	if err != nil {
		frame.Write(rw, frame.Error, []byte("this device isn't sharing its screen"))
		return err
	}
	defer v.Close()
	done := make(chan error, 1)
	go func() { // vnc → peer
		buf := make([]byte, 64*1024)
		for {
			n, err := v.Read(buf)
			if n > 0 {
				if werr := frame.Write(rw, frame.Data, buf[:n]); werr != nil {
					done <- werr
					return
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	go func() { // peer → vnc
		for {
			typ, p, err := frame.Read(rw)
			if err != nil {
				done <- err
				return
			}
			if typ == frame.Data {
				if _, err := v.Write(p); err != nil {
					done <- err
					return
				}
			}
		}
	}()
	err = <-done
	if err == io.EOF {
		return nil
	}
	return err
}
