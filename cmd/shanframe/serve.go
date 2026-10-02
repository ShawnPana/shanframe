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
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/shawnpana/shanframe/internal/android"
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
	conns    map[string]*peer.Conn                    // session → connection
	stops    map[string]func()                        // session → screen-capture teardown
	starts   map[string]func(screencap.Display) error // session → start its capture (once the screen stream names a display)
}

// osPretty and hwModel are collected once: cheap probes, stable answers.
var (
	osPretty = setup.OSPretty()
	hwModel  = setup.Model()
)

func serve() error {
	peer.LogWriter = log.Writer()  // pion's internals go to the agent log, never a terminal
	update.Guard(30 * time.Second) // confirm this build alive, or roll back
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	a := &agent{cfg: cfg, conns: map[string]*peer.Conn{}, stops: map[string]func(){}, starts: map[string]func(screencap.Display) error{}}
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
	// whatever the frameworks opened during setup stays out of children and
	// of any re-exec (see update.MarkCloseOnExec)
	update.MarkCloseOnExec()
	go func() {
		waiting := 0 // minutes spent waiting on a permission grant
		for range time.Tick(time.Minute) {
			changed := a.ensureReady()
			if changed {
				a.rz.Send(rendezvous.Msg{T: "device", Device: a.device()})
			}
			update.MarkCloseOnExec() // the permission probe may have opened something new
			a.mu.Lock()
			ready := a.screen.Ready
			a.mu.Unlock()
			switch {
			case !nativeScreen() || a.busy():
				continue
			case ready && changed:
				// macOS applies a Screen Recording grant to a process at its
				// start: the probe says yes now, capture works after a re-exec
				log.Printf("screen permission granted; restarting once to apply it")
				update.Restart()
			case !ready:
				// a Mac that never gets the grant used to re-exec every two
				// minutes for the life of the machine; the probe reads the
				// grant live, so a slow safety re-exec is all that's needed
				if waiting++; waiting >= 60 {
					waiting = 0
					log.Printf("still waiting on screen permission; restarting in case the grant isn't visible to this process")
					update.Restart()
				}
			default:
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
	if os.Getenv("SHANFRAME_NO_UPDATE") == "" { // dev knob: run a local build without it being replaced
		if !android.Available() && setup.ServiceInstalled() {
			// a service-run agent (systemd, launchd) whose binary sits somewhere
			// it can't write (install.sh's /usr/local/bin) reinstalls itself
			// from a place it can
			logPath := filepath.Join(configDir(), "serve.log")
			update.Relocate = func(exe string) error { return setup.InstallService(exe, logPath) }
		}
		go update.Loop(cfg.Server, cfg.Token, "shanframe", 5*time.Minute, a.busy)
	}
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
		OSName: osPretty, Arch: runtime.GOARCH, Model: hwModel, Build: build,
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
			changed := a.startCmd != v
			a.startCmd = v
			a.mu.Unlock()
			if changed { // it is typed into every new shell: never change silently
				log.Printf("start command is now %q", v)
			}
		}
	case "offer":
		a.mu.Lock()
		ice := a.ice
		a.mu.Unlock()
		from, session := m.From, m.Session
		a.mu.Lock()
		screenReady := a.screen.Ready
		a.mu.Unlock()
		var video func(*webrtc.PeerConnection) error
		if nativeScreen() && screenReady {
			video = func(pc *webrtc.PeerConnection) error {
				start, stop, err := attachScreen(pc)
				if err != nil { // terminal and exec still work; the viewer shows no picture
					log.Printf("session %s: screen: %v", session, err)
					return nil
				}
				a.mu.Lock()
				a.stops[session] = stop
				a.starts[session] = start
				a.mu.Unlock()
				return nil
			}
		}
		// who is calling, as the server vouches for it — so this machine's own
		// log answers "who was here" without asking anyone
		caller := "an unnamed caller"
		if m.Caller != nil && m.Caller.Name == "" {
			caller = "a controller on this account (names are withheld from a one-way machine)"
		} else if m.Caller != nil {
			caller = fmt.Sprintf("%q", m.Caller.Name)
			if m.Caller.Kind == "agent" {
				caller += " (another of your machines)"
			}
		}
		// a one-way machine reaching through a grant: the server vouches for
		// exactly what it may open here, and nothing else gets a stream —
		// not a shell, not another port. This is the enforcement point:
		// streams are end to end, so the server can't check them.
		grants := m.Grants
		if len(grants) > 0 {
			caller += fmt.Sprintf(" (granted %s)", grantList(grants))
			video = nil
		}
		log.Printf("session %s opened by %s", session, caller)
		serve := func(open rendezvous.Open, s io.ReadWriteCloser) {
			what := open.Service
			if open.Service == "tcp" {
				what = fmt.Sprintf("tcp to %s:%d", open.Host, open.Port)
			}
			if len(grants) > 0 && !grantAllows(grants, open) {
				log.Printf("%s ← %s: REFUSED, beyond its grant", what, caller)
				s.Write(append([]byte{1}, "not allowed: this device may only reach "+grantList(grants)...))
				s.Close()
				return
			}
			log.Printf("%s ← %s", what, caller)
			a.handleStream(open, s, session)
		}
		conn, answer, err := peer.Answer(ice, m.SDP, serve, video,
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
	delete(a.starts, session)
	a.mu.Unlock()
	if stop != nil {
		stop()
	}
	if conn != nil {
		conn.Close()
	}
}

// handleStream serves one service over one DataChannel stream.
func (a *agent) handleStream(open rendezvous.Open, s io.ReadWriteCloser, session string) {
	defer s.Close()
	switch open.Service {
	case "info":
		b, _ := json.Marshal(map[string]any{"screen": a.device(), "displays": screencap.Displays()})
		frame.Write(s, frame.Data, b)
		return
	case "exec":
		if open.Priv {
			servePrivExec(s, open.Cmd)
			return
		}
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
		png, w, h, err := screencap.Still(open.Display)
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
		displays := screencap.Displays()
		d, err := screencap.Pick(displays, open.Display)
		if err != nil {
			frame.Write(s, frame.Error, []byte(err.Error()))
			return
		}
		a.mu.Lock()
		start := a.starts[session] // nil when the session carries no video (CLI verbs)
		delete(a.starts, session)
		a.mu.Unlock()
		if start != nil {
			if err := start(d); err != nil {
				log.Printf("screen: %v", err)
				frame.Write(s, frame.Error, []byte(err.Error()))
				return
			}
		}
		if err := serveScreenInput(s, d, displays); err != nil && err != io.EOF && !strings.Contains(err.Error(), "abort chunk") {
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
