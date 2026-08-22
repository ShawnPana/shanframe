// shanframe: one binary, one device on your list, terminals and screens
// everywhere.
//
//	shanframe join <server> <token> [--name X]  point this device at your server
//	shanframe serve                              run as a device (foreground)
//	shanframe install                            run `serve` as a service (boot/login)
//	shanframe uninstall                          remove the service
//	shanframe ls                                 list your devices
//	shanframe <device>                           open a shell on that device
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/shawnpana/shanframe/internal/frame"
	"github.com/shawnpana/shanframe/internal/peer"
	"github.com/shawnpana/shanframe/internal/rendezvous"
	"github.com/shawnpana/shanframe/internal/setup"
	"github.com/shawnpana/shanframe/internal/update"
	"golang.org/x/term"
)

// build is stamped by scripts/release.sh (-X main.build=<git sha>).
var build = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case update.ProbeArg: // update preflight: prove the binary starts, exit 0
		return
	case "join":
		server := "https://api.shanframe.com"
		name := ""
		rest := os.Args[2:]
		for i := 0; i < len(rest); i++ {
			if rest[i] == "--name" && i+1 < len(rest) {
				name = rest[i+1]
				i++
			} else {
				server = rest[i]
			}
		}
		var c Config
		if c, err = join(server, name); err == nil {
			fmt.Printf("linked: this machine is %q on your shanframe\nnext: shanframe install\n", c.Name)
		}
	case "serve":
		err = serve()
	case "install":
		err = install()
	case "uninstall":
		if err = setup.UninstallService(); err == nil {
			fmt.Println("removed", setup.ServiceDescription())
		}
	case "ls":
		err = ls(len(os.Args) > 2 && os.Args[2] == "--json")
	case "rm":
		if len(os.Args) < 3 {
			usage()
		}
		err = rm(strings.Join(os.Args[2:], " "))
	case "rename":
		if len(os.Args) < 3 {
			usage()
		}
		err = rename(strings.Join(os.Args[2:], " "))
	case "startcmd": // startcmd <device> [value…|--clear]
		if len(os.Args) < 3 {
			usage()
		}
		err = startcmd(os.Args[2], os.Args[3:])
	case "connect":
		if len(os.Args) < 3 {
			usage()
		}
		err = connect(os.Args[2])
	case "run": // run <device> -- cmd
		if len(os.Args) < 4 {
			usage()
		}
		err = run(os.Args[2], stripDashes(os.Args[3:]))
	case "tunnels": // kept tunnels on this machine
		ts := loadTunnels()
		if len(ts) == 0 {
			fmt.Println("no kept tunnels — `shanframe <device> tunnel <port> --install` keeps one")
		}
		for _, t := range ts {
			fmt.Printf("%-20s %s\n", t.Device, strings.Join(t.Forwards, "  "))
		}
	case "tunnel": // tunnel <device> <spec>... | --socks <port>
		if len(os.Args) < 4 {
			usage()
		}
		err = tunnel(os.Args[2], os.Args[3:])
	case "_screencap": // hidden: capture N seconds of H.264 to a file (debug)
		err = screencapDebug(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default: // <device> [action ...]
		target, rest := os.Args[1], os.Args[2:]
		switch {
		case len(rest) == 0, rest[0] == "term":
			err = connect(target)
		case rest[0] == "run":
			if len(rest) < 2 {
				usage()
			}
			err = run(target, stripDashes(rest[1:]))
		case rest[0] == "tunnel":
			if len(rest) < 2 {
				usage()
			}
			err = tunnel(target, rest[1:])
		case rest[0] == "startcmd":
			err = startcmd(target, rest[1:])
		case rest[0] == "cdp":
			err = cdp(target, rest[1:])
		case screenVerbs[rest[0]]:
			err = screenVerb(target, rest[0], rest[1:])
		default:
			err = fmt.Errorf("unknown action %q for %s (actions: term, run, tunnel, cdp, screenshot, click, tap, drag, scroll, type, key, size, batch)", rest[0], target)
		}
	}
	if err != nil {
		var code exitCode
		if errors.As(err, &code) {
			os.Exit(int(code))
		}
		fmt.Fprintln(os.Stderr, "shanframe:", err)
		os.Exit(1)
	}
}

var screenVerbs = map[string]bool{"screenshot": true, "size": true, "click": true, "tap": true, "rightclick": true,
	"doubleclick": true, "dblclick": true, "middleclick": true, "drag": true, "swipe": true, "scroll": true, "move": true,
	"type": true, "key": true, "batch": true}

// startcmd shows, sets, or clears the device's terminal startup command —
// the same account-wide setting the app edits.
func startcmd(target string, args []string) error {
	c, cancel, err := newClient()
	if err != nil {
		return err
	}
	defer cancel()
	dev, err := c.target(target)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		if dev.StartCmd == "" {
			fmt.Printf("%s: no startup command\n", dev.Name)
		} else {
			fmt.Printf("%s: %s\n", dev.Name, dev.StartCmd)
		}
		return nil
	}
	v := strings.Join(args, " ")
	if v == "--clear" {
		v = ""
	}
	if err := c.rz.Send(rendezvous.Msg{T: "set", To: dev.ID, Set: map[string]string{"startCmd": v}}); err != nil {
		return err
	}
	select { // the server answers with a fresh device list once it stored it
	case devs := <-c.devs:
		for _, d := range devs {
			if d.ID == dev.ID {
				if d.StartCmd == "" {
					fmt.Printf("%s: startup command cleared\n", d.Name)
				} else {
					fmt.Printf("%s: every new terminal runs %q first\n", d.Name, d.StartCmd)
				}
				return nil
			}
		}
	case <-time.After(10 * time.Second):
	}
	return errors.New("the server didn't confirm — check `shanframe ls --json`")
}

func stripDashes(a []string) []string {
	if len(a) > 0 && a[0] == "--" {
		return a[1:]
	}
	return a
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: shanframe <command>
  join [server] [--name X]           link this machine to your account
  install | uninstall                run as a service / remove the service
  serve                              run in the foreground
  ls [--json]                        your devices (and what each offers)
  <device>                           shell on that device (name prefix works)
  <device> run -- <command>          run one command there; stdout/stderr/exit code pass through
  <device> tunnel <port>             local :port → that device's localhost:port (also lport:rport, lport:host:rport)
  <device> tunnel --socks <port>     local SOCKS5 proxy; connections exit from that device's network
      … --install | --uninstall      keep it: the background service listens from now on, connects on use
  tunnels                            kept tunnels on this machine
  <device> screenshot [file|-]       one PNG of its screen (point coordinates), default screenshot.png
  <device> click|tap X Y [--right|--double]   drag X1 Y1 X2 Y2   scroll X Y DY   type TEXT   key cmd+shift+t
  <device> batch                     many of the above from stdin, one per line, over one connection
  <device> cdp [--port N] [--local N]   its Chrome's DevTools as localhost here; prints CDP_WS/CDP_URL to use
  <device> startcmd [CMD | --clear]  what every new terminal on it runs first (account-wide); no args shows it
  rm <device>                        remove a device from your list (offline only)
  rename <new-name>                  rename this machine`)
	os.Exit(2)
}

// install runs the daemon as a service so the device stays reachable across
// reboots — no terminal, no nohup.
func install() error {
	if _, err := loadConfig(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	logPath := filepath.Join(configDir(), "serve.log")
	if err := setup.InstallService(exe, logPath); err != nil {
		return err
	}
	fmt.Printf("installed %s\n  runs: %s serve\n  log:  %s\n", setup.ServiceDescription(), exe, setup.LogHint(logPath))
	waitForReady()
	return nil
}

// waitForReady stays on the screen until the service just installed shows up
// online — the moment a user can actually reach this machine — so "install"
// ends with a fact, not a hope. On macOS it keeps waiting for the screen:
// the OS grants Screen Recording and Accessibility to the process that asks
// (the service launchd just started), not to this terminal, so the prompts
// come from the service and readiness is read back from the device list.
func waitForReady() {
	fmt.Println()
	fmt.Println("waiting for it to come online…")
	c, cancel, err := newClient()
	if err != nil {
		fmt.Println("  (couldn't check from here:", err, "— the app shows the live state)")
		return
	}
	defer cancel()
	online := time.After(45 * time.Second)
	var screen <-chan time.Time
	explained, lastNote := false, ""
	// The first list is a snapshot that may still show the *previous* service
	// (a reinstall); only a later broadcast proves the one just installed.
	snapshot := true
	for {
		select {
		case devs := <-c.devs:
			if snapshot {
				snapshot = false
				continue
			}
			for _, d := range devs {
				if d.ID != c.cfg.DeviceID || !d.Online {
					continue
				}
				if !nativeScreen() {
					fmt.Printf("✓ %q is online — open the app on any device to use it\n", d.Name)
					return
				}
				if d.Screen {
					fmt.Printf("✓ %q is online: terminal and desktop ready\n", d.Name)
					return
				}
				if !explained {
					explained = true
					screen = time.After(2 * time.Minute)
					fmt.Printf("✓ %q is online (terminal)\n", d.Name)
					fmt.Println()
					fmt.Println("macOS will now ask to let shanframe record the screen and control this Mac.")
					fmt.Println("Allow both (System Settings → Privacy & Security) — it's what lets you see and")
					fmt.Println("use this Mac from your other devices. Waiting…")
				}
				if d.Note != "" && d.Note != lastNote {
					lastNote = d.Note
					fmt.Println("  …", d.Note)
				}
			}
		case <-online:
			if explained {
				continue
			}
			fmt.Println("  not online yet — check the log above; the app shows the live state")
			return
		case <-screen:
			fmt.Println("  not finished yet — that's fine. Once both are allowed this Mac becomes ready")
			fmt.Println("  on its own within a couple of minutes; the app shows what's still missing.")
			return
		}
	}
}

// client is the CLI as a controller: one rendezvous connection, sessions on demand.
type client struct {
	cfg  Config
	rz   *rendezvous.Client
	ice  []rendezvous.ICEServer
	devs chan []rendezvous.Device
	sess map[string]func(rendezvous.Msg)
}

func newClient() (*client, context.CancelFunc, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, nil, err
	}
	c := &client{cfg: cfg, devs: make(chan []rendezvous.Device, 1), sess: map[string]func(rendezvous.Msg){}}
	unauth := make(chan struct{}, 1)
	c.rz = &rendezvous.Client{URL: cfg.wsURL(), Token: cfg.Token,
		Hello: rendezvous.Msg{T: "hello", Kind: rendezvous.KindClient}, OnMsg: c.onMsg,
		OnUnauthorized: func() { unauth <- struct{}{} }}
	ctx, cancel := context.WithCancel(context.Background())
	go c.rz.Run(ctx)
	select {
	case devs := <-c.devs:
		c.devs <- devs
	case <-unauth:
		cancel()
		return nil, nil, rendezvous.ErrUnauthorized
	case <-time.After(15 * time.Second):
		cancel()
		return nil, nil, fmt.Errorf("can't reach %s", cfg.Server)
	}
	return c, cancel, nil
}

func (c *client) onMsg(m rendezvous.Msg) {
	switch m.T {
	case "hello":
		c.ice = m.ICEServers
	case "devices":
		select {
		case c.devs <- m.Devices:
		default:
		}
	default:
		if h, ok := c.sess[m.Session]; ok {
			h(m)
		}
	}
}

func ls(asJSON bool) error {
	c, cancel, err := newClient()
	if err != nil {
		return err
	}
	defer cancel()
	devs := <-c.devs
	if asJSON {
		if devs == nil {
			devs = []rendezvous.Device{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(devs)
	}
	if len(devs) == 0 {
		fmt.Println("no devices yet — run `shanframe join` + `shanframe install` on a computer")
		return nil
	}
	for _, d := range devs {
		state := "offline"
		if d.Asleep {
			state = "asleep"
		}
		if d.Online {
			state = "online"
			if d.Screen {
				state += ", screen"
			}
		}
		me := ""
		if d.ID == c.cfg.DeviceID {
			me = "  (this device)"
		}
		fmt.Printf("%-16s %-8s %s%s\n", d.Name, d.OS, state, me)
	}
	return nil
}

// open starts a session of one service on a device and returns its stream.
func (c *client) open(dev *rendezvous.Device, open rendezvous.Open) (io.ReadWriteCloser, func(), error) {
	s, conn, err := c.openConn(dev, open)
	if err != nil {
		return nil, nil, err
	}
	return s, func() { s.Close(); conn.Close() }, nil
}

// openConn is open, also handing back the session for more streams (tunnels).
func (c *client) openConn(dev *rendezvous.Device, open rendezvous.Open) (io.ReadWriteCloser, *peer.Conn, error) {
	if !dev.Online {
		return nil, nil, fmt.Errorf("%s is offline", dev.Name)
	}
	session := fmt.Sprintf("cli-%d", time.Now().UnixNano())
	ready := make(chan io.ReadWriteCloser, 1)
	var conn *peer.Conn
	c.sess[session] = func(m rendezvous.Msg) {
		switch m.T {
		case "answer":
			conn.SetAnswer(m.SDP)
		case "ice":
			conn.AddCandidate(m.Candidate)
		case "error":
			fmt.Fprintln(os.Stderr, "shanframe:", m.Error)
		}
	}
	conn, offer, err := peer.Offer(c.ice, open,
		func(cand string) { c.rz.Send(rendezvous.Msg{T: "ice", To: dev.ID, Session: session, Candidate: cand}) },
		func(s io.ReadWriteCloser) { ready <- s },
		nil)
	if err != nil {
		return nil, nil, err
	}
	if err := c.rz.Send(rendezvous.Msg{T: "offer", To: dev.ID, Session: session, SDP: offer}); err != nil {
		conn.Close()
		return nil, nil, err
	}
	select {
	case s := <-ready:
		return s, conn, nil
	case <-time.After(20 * time.Second):
		conn.Close()
		return nil, nil, errors.New("could not connect to " + dev.Name)
	}
}

// target resolves a device argument against the live list.
func (c *client) target(q string) (*rendezvous.Device, error) {
	devs := <-c.devs
	return resolveDevice(devs, q)
}

func connect(target string) error {
	c, cancel, err := newClient()
	if err != nil {
		return err
	}
	defer cancel()
	dev, err := c.target(target)
	if err != nil {
		return err
	}
	fd := int(os.Stdin.Fd())
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		cols, rows = 80, 24
	}
	s, done, err := c.open(dev, rendezvous.Open{Service: "shell", Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	defer done()

	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, old)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	go func() {
		for range sig {
			if c2, r2, err := term.GetSize(fd); err == nil {
				frame.Write(s, frame.Resize, frame.ResizePayload(c2, r2))
			}
		}
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if frame.Write(s, frame.Data, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		typ, p, err := frame.Read(s)
		if err != nil {
			fmt.Fprintf(os.Stdout, "\r\n[shanframe: connection to %s closed]\r\n", dev.Name)
			return nil
		}
		switch typ {
		case frame.Data:
			os.Stdout.Write(p)
		case frame.Error:
			term.Restore(fd, old)
			return fmt.Errorf("%s", p)
		}
	}
}

// exitCode is how `run` hands the remote exit status to main.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

// run executes one command line on a device: stdout/stderr/exit code pass
// through, stdin is piped when it isn't a terminal. ssh semantics for agents.
func run(target string, args []string) error {
	c, cancel, err := newClient()
	if err != nil {
		return err
	}
	defer cancel()
	dev, err := c.target(target)
	if err != nil {
		return err
	}
	s, done, err := c.open(dev, rendezvous.Open{Service: "exec", Cmd: strings.Join(args, " ")})
	if err != nil {
		return err
	}
	defer done()
	go func() {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			frame.Write(s, frame.Eof, nil) // interactive: don't make the command wait on a keyboard
			return
		}
		buf := make([]byte, 32<<10)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if frame.Write(s, frame.Data, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				frame.Write(s, frame.Eof, nil)
				return
			}
		}
	}()
	for {
		typ, p, err := frame.Read(s)
		if err != nil {
			return fmt.Errorf("connection to %s closed", dev.Name)
		}
		switch typ {
		case frame.Data:
			os.Stdout.Write(p)
		case frame.Stderr:
			os.Stderr.Write(p)
		case frame.Error:
			return fmt.Errorf("%s", p)
		case frame.Exit:
			if len(p) == 4 {
				return exitCode(binary.BigEndian.Uint32(p))
			}
			return exitCode(255)
		}
	}
}

// resolveDevice finds a device by exact name or id, else unique
// case-insensitive prefix ("rasp" → "Raspberry Pi 5").
func resolveDevice(devs []rendezvous.Device, q string) (*rendezvous.Device, error) {
	lower := strings.ToLower(q)
	var prefix []*rendezvous.Device
	for i := range devs {
		n := strings.ToLower(devs[i].Name)
		if n == lower || devs[i].ID == q {
			return &devs[i], nil
		}
		if strings.HasPrefix(n, lower) {
			prefix = append(prefix, &devs[i])
		}
	}
	switch len(prefix) {
	case 1:
		return prefix[0], nil
	case 0:
		return nil, fmt.Errorf("no device named %q (try `shanframe ls`)", q)
	default:
		names := make([]string, len(prefix))
		for i, d := range prefix {
			names[i] = d.Name
		}
		return nil, fmt.Errorf("%q matches several devices: %s", q, strings.Join(names, ", "))
	}
}

// rm removes a device from the list. Offline devices only: a live agent
// would just re-register — uninstall shanframe on it instead.
func rm(target string) error {
	c, cancel, err := newClient()
	if err != nil {
		return err
	}
	defer cancel()
	devs := <-c.devs
	dev, err := resolveDevice(devs, target)
	if err != nil {
		return err
	}
	if dev.Online {
		return fmt.Errorf("%s is online — run `shanframe uninstall` on it first", dev.Name)
	}
	session := fmt.Sprintf("rm-%d", time.Now().UnixNano())
	done := make(chan error, 1)
	c.sess[session] = func(m rendezvous.Msg) {
		if m.Error != "" {
			done <- errors.New(m.Error)
		} else {
			done <- nil
		}
	}
	if err := c.rz.Send(rendezvous.Msg{T: "rm", To: dev.ID, Session: session}); err != nil {
		return err
	}
	select {
	case err := <-done:
		if err != nil {
			return err
		}
	case <-time.After(10 * time.Second):
		return errors.New("no answer from the server")
	}
	fmt.Printf("removed %s\n", dev.Name)
	return nil
}

// rename changes this machine's name and restarts the agent so the list
// updates everywhere.
func rename(name string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	cfg.Name = name
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("renamed to %q\n", name)
	if err := setup.RestartService(); err != nil {
		fmt.Println("restart the agent to make it show everywhere:", setup.LogHint(""))
	}
	return nil
}
