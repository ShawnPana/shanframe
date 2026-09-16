// Package android makes an Android phone a screen target without an app of
// its own. The agent runs in a terminal environment on the phone (Termux);
// the phone's screen and input live behind the `shell` uid, which the agent
// reaches through a broker — this same binary, started once per boot as that
// uid through the phone's own Wireless debugging (adb to 127.0.0.1). On top
// of that it drives scrcpy's server (an Apache-2.0 JAR, embedded here):
// H.264 of the display out, touches/keys/text in. No root, no consent dialog
// beyond the one-time pairing code Android itself requires.
package android

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Available reports whether this process runs on Android (Termux or any
// Linux userland on the phone). Decided once.
func Available() bool { return available }

var available = func() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := os.Stat("/system/build.prop")
	return err == nil
}()

// adbPath finds adb: PATH first, then Termux's prefix (the service may run
// with a bare environment).
func adbPath() string {
	p, err := LookPath("adb")
	if err != nil {
		return ""
	}
	return p
}

// HasADB reports whether the adb client is installed on the phone.
func HasADB() bool { return adbPath() != "" }

// InstallADB installs the adb client through the phone's package manager
// (Termux). Bounded; errors are logged, not fatal — readiness just stays off.
func InstallADB() error {
	pkg, err := LookPath("pkg")
	if err != nil {
		return errors.New("no package manager to install adb with")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, pkg, "install", "-y", "android-tools").CombinedOutput()
	if err != nil {
		return fmt.Errorf("pkg install android-tools: %v: %s", err, tail(out))
	}
	return nil
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 && len(s)-i < 200 {
		return s[i+1:]
	}
	if len(s) > 200 {
		return s[len(s)-200:]
	}
	return s
}

// adb runs one adb command with a timeout and returns its combined output.
// Only pairing and the per-boot broker bootstrap use adb; everything else
// goes through the broker.
func adb(timeout time.Duration, args ...string) (string, error) {
	p := adbPath()
	if p == "" {
		return "", errors.New("adb is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, p, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Connected reports whether adb is attached to this phone's own debugging
// daemon (a 127.0.0.1:PORT entry in the "device" state).
func Connected() bool {
	out, err := adb(10*time.Second, "devices")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && strings.HasPrefix(f[0], "127.0.0.1:") && f[1] == "device" {
			return true
		}
	}
	return false
}

// Wireless debugging listens on a random port in this range — one for the
// connection itself, another (only while the pairing dialog is open) for
// pairing. Android doesn't tell a local process which; a loopback scan
// finds the candidates in well under a second.
const scanLo, scanHi = 30000, 49999

// openLoopbackPorts lists ports in [scanLo, scanHi] with a listener on
// 127.0.0.1, lowest first.
func openLoopbackPorts() []int {
	var mu sync.Mutex
	var open []int
	ports := make(chan int, 256)
	var wg sync.WaitGroup
	for i := 0; i < 256; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range ports {
				c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p), 300*time.Millisecond)
				if err == nil {
					c.Close()
					mu.Lock()
					open = append(open, p)
					mu.Unlock()
				}
			}
		}()
	}
	for p := scanLo; p <= scanHi; p++ {
		ports <- p
	}
	close(ports)
	wg.Wait()
	for i := 1; i < len(open); i++ { // stable order for deterministic attempts
		for j := i; j > 0 && open[j] < open[j-1]; j-- {
			open[j], open[j-1] = open[j-1], open[j]
		}
	}
	return open
}

// ErrNotPaired is returned by Connect when the phone has a debugging
// listener but doesn't trust this agent yet (no pairing done).
var ErrNotPaired = errors.New("not paired")

// ErrNoListener is returned by Connect when no debugging listener exists
// (Wireless debugging is off, or the phone isn't on an allowed Wi‑Fi).
var ErrNoListener = errors.New("wireless debugging is off")

// Connect attaches adb to the phone's own debugging daemon. Idempotent.
func Connect() error {
	if Connected() {
		return nil
	}
	adb(10*time.Second, "start-server")
	sawListener := false
	for _, p := range openLoopbackPorts() {
		out, _ := adb(15*time.Second, "connect", fmt.Sprintf("127.0.0.1:%d", p))
		switch {
		case strings.Contains(out, "connected to"):
			return nil
		case strings.Contains(out, "failed to authenticate"), strings.Contains(out, "unauthorized"):
			sawListener = true
		}
	}
	if sawListener {
		return ErrNotPaired
	}
	return ErrNoListener
}

// Pair trusts this agent with the phone's debugging daemon using the code
// from Settings → Developer options → Wireless debugging → "Pair device
// with pairing code" (the dialog must stay open). Then connects and starts
// the broker.
func Pair(code string) error {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return errors.New("the pairing code is 6 digits")
	}
	adb(10*time.Second, "start-server")
	var last string
	for _, p := range openLoopbackPorts() {
		out, _ := adb(20*time.Second, "pair", fmt.Sprintf("127.0.0.1:%d", p), code)
		if strings.Contains(out, "Successfully paired") {
			if err := Connect(); err != nil { // the connect listener is a different port
				return fmt.Errorf("paired, but couldn't connect: %w", err)
			}
			return Bootstrap()
		}
		if out != "" {
			last = out
		}
	}
	if last == "" {
		return errors.New("no pairing dialog found — open 'Pair device with pairing code' and keep it on screen")
	}
	return fmt.Errorf("pairing failed: %s", tail([]byte(last)))
}

// DisplaySize is the screen's logical size in pixels, in its current
// orientation. A live video stream is authoritative; otherwise the window
// manager is asked.
func DisplaySize() (w, h int) {
	if s := liveStream(); s != nil {
		if w, h := s.Size(); w > 0 && h > 0 {
			return w, h
		}
	}
	return wmSize()
}

// wmSize asks the window manager for the logical size in the current
// orientation.
func wmSize() (w, h int) {
	out, err := brokerExec(10*time.Second, "wm", "size")
	if err != nil {
		return 0, 0
	}
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(l, "Override size:"); ok { // an explicit wm size wins
			w, h = parseSize(v)
		} else if v, ok := strings.CutPrefix(l, "Physical size:"); ok && w == 0 {
			w, h = parseSize(v)
		}
	}
	if rot := rotation(); rot == 1 || rot == 3 {
		w, h = h, w
	}
	return w, h
}

// rotation is the display's current rotation quadrant (0..3).
func rotation() int {
	out, err := brokerExec(10*time.Second, "dumpsys", "display")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "mCurrentOrientation="); ok {
			r, _ := strconv.Atoi(strings.TrimSpace(v))
			return r
		}
	}
	return 0
}

func parseSize(s string) (int, int) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "x")
	if !ok {
		return 0, 0
	}
	w, _ := strconv.Atoi(a)
	h, _ := strconv.Atoi(b)
	return w, h
}

// Screenshot grabs the display as PNG with its pixel size.
func Screenshot() (png []byte, w, h int, err error) {
	png, err = brokerExec(20*time.Second, "screencap", "-p")
	if err != nil {
		return nil, 0, 0, err
	}
	if len(png) < 24 || string(png[1:4]) != "PNG" {
		return nil, 0, 0, errors.New("screencap returned no image (is the phone on?)")
	}
	w = int(binary.BigEndian.Uint32(png[16:20]))
	h = int(binary.BigEndian.Uint32(png[20:24]))
	return png, w, h, nil
}

// --- the scrcpy server: one process per use, started by the broker and
// reached through it (it proxies the server's abstract socket).

const (
	scrcpyVersion = "4.1"
	jarOnPhone    = "/data/local/tmp/shanframe-scrcpy-server.jar"
)

// server is one running scrcpy server.
type server struct {
	scid string
	pid  int
}

// startServer launches the server with opts. The caller then dials once per
// enabled stream, in scrcpy's order (video, then control); the first
// accepted socket starts with a dummy byte.
func startServer(opts ...string) (*server, error) {
	var b [4]byte
	if f, err := os.Open("/dev/urandom"); err == nil {
		f.Read(b[:])
		f.Close()
	}
	scid := fmt.Sprintf("%08x", binary.BigEndian.Uint32(b[:])&0x7fffffff)
	c, resp, err := call(brokerReq{Op: "scrcpy-start", SCID: scid, Args: opts})
	if err != nil {
		return nil, fmt.Errorf("scrcpy: %w", err)
	}
	c.Close()
	return &server{scid: scid, pid: resp.PID}, nil
}

// dial connects one stream to the server. first=true expects (and strips)
// the dummy byte the server writes on its first accepted socket.
func (s *server) dial(first bool) (net.Conn, error) {
	c, _, err := call(brokerReq{Op: "scrcpy-dial", SCID: s.scid})
	if err != nil {
		return nil, err
	}
	if first {
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		var b [1]byte
		if _, err := c.Read(b[:]); err != nil {
			c.Close()
			return nil, fmt.Errorf("scrcpy server: %w", err)
		}
		c.SetReadDeadline(time.Time{})
	}
	return c, nil
}

func (s *server) stop() {
	if s.pid > 0 { // it exits on its own when its sockets close; this is belt and braces
		brokerExec(5*time.Second, "kill", strconv.Itoa(s.pid))
	}
}
