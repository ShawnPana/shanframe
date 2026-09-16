package android

// The broker is this same binary running as Android's `shell` user — the
// one identity allowed to read the screen and inject input without an app.
// It is started once per boot through the phone's own Wireless debugging
// (adb), detached so it outlives that connection, and from then on the agent
// (an ordinary app user in Termux) talks to it over loopback with a per-boot
// token. Wi‑Fi, adb and Wireless debugging are only needed again after the
// phone restarts; the screen keeps working on any network in between.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	brokerBin = "/data/local/tmp/shanframe-broker"
	brokerLog = "/data/local/tmp/shanframe-broker.log"
)

// StateDir is where the agent keeps the broker's port and token (set by the
// command from its config dir before any use).
var StateDir = func() string {
	if d := os.Getenv("SHANFRAME_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "shanframe")
}()

type brokerState struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	Build string `json:"build,omitempty"`
}

func statePath() string { return filepath.Join(StateDir, "android-broker.json") }

func loadState() (brokerState, bool) {
	var st brokerState
	b, err := os.ReadFile(statePath())
	if err != nil || json.Unmarshal(b, &st) != nil || st.Port == 0 || st.Token == "" {
		return brokerState{}, false
	}
	return st, true
}

func saveState(st brokerState) error {
	os.MkdirAll(StateDir, 0o700)
	b, _ := json.Marshal(st)
	return os.WriteFile(statePath(), b, 0o600)
}

// --- wire: one JSON request line, one JSON response line, then a body
// (exec output, or a raw proxied socket for scrcpy-dial).

type brokerReq struct {
	Token string   `json:"token"`
	Op    string   `json:"op"`
	Args  []string `json:"args,omitempty"`
	SCID  string   `json:"scid,omitempty"`
}

type brokerResp struct {
	OK   bool   `json:"ok"`
	Err  string `json:"err,omitempty"`
	Code int    `json:"code,omitempty"` // exec exit code
	PID  int    `json:"pid,omitempty"`  // scrcpy-start
	Up   int64  `json:"up,omitempty"`   // ping: broker start time (unix)
	Bld  string `json:"build,omitempty"`
}

// brokerConn is a client connection after the response header: the rest of
// the stream is the body (bufio may have read ahead).
type brokerConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *brokerConn) Read(p []byte) (int, error) { return c.r.Read(p) }

var (
	brokerMu   sync.Mutex
	brokerOKAt time.Time
)

// call opens one broker request. On success the returned conn's remaining
// stream is the body; the caller closes it.
func call(req brokerReq) (*brokerConn, brokerResp, error) {
	st, ok := loadState()
	if !ok {
		return nil, brokerResp{}, errors.New("no broker")
	}
	req.Token = st.Token
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", st.Port), 2*time.Second)
	if err != nil {
		return nil, brokerResp{}, fmt.Errorf("broker: %w", err)
	}
	c.SetDeadline(time.Now().Add(30 * time.Second))
	b, _ := json.Marshal(req)
	if _, err := c.Write(append(b, '\n')); err != nil {
		c.Close()
		return nil, brokerResp{}, err
	}
	r := bufio.NewReader(c)
	line, err := r.ReadBytes('\n')
	if err != nil {
		c.Close()
		return nil, brokerResp{}, fmt.Errorf("broker: %w", err)
	}
	var resp brokerResp
	if json.Unmarshal(line, &resp) != nil {
		c.Close()
		return nil, brokerResp{}, errors.New("broker: bad response")
	}
	if !resp.OK {
		c.Close()
		return nil, resp, errors.New(resp.Err)
	}
	c.SetDeadline(time.Time{})
	return &brokerConn{Conn: c, r: r}, resp, nil
}

// brokerAlive pings the broker (cached for a few seconds: readiness asks often).
func brokerAlive() bool {
	brokerMu.Lock()
	if time.Since(brokerOKAt) < 5*time.Second {
		brokerMu.Unlock()
		return true
	}
	brokerMu.Unlock()
	c, _, err := call(brokerReq{Op: "ping"})
	if err != nil {
		return false
	}
	c.Close()
	brokerMu.Lock()
	brokerOKAt = time.Now()
	brokerMu.Unlock()
	return true
}

// brokerExec runs a command as the shell user and returns its output.
func brokerExec(timeout time.Duration, args ...string) ([]byte, error) {
	c, resp, err := call(brokerReq{Op: "exec", Args: args})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(timeout))
	out, err := io.ReadAll(c)
	if err != nil {
		return out, err
	}
	if resp.Code != 0 {
		return out, fmt.Errorf("%s: exit %d: %s", args[0], resp.Code, tail(out))
	}
	return out, nil
}

// --- bootstrap (needs adb, i.e. Wireless debugging on an allowed Wi‑Fi)

// Bootstrap pushes this binary to the phone's shell-accessible temp dir and
// starts it as the broker, detached. Requires an adb connection.
func Bootstrap() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	var tok [16]byte
	rand.Read(tok[:])
	token := hex.EncodeToString(tok[:])
	if out, err := adb(2*time.Minute, "push", exe, brokerBin+".new"); err != nil {
		return fmt.Errorf("push broker: %v: %s", err, tail([]byte(out)))
	}
	// old broker (from before an agent update) goes away; the new one replaces it
	// ([s] so the pattern doesn't match the shell running this very command)
	adb(10*time.Second, "shell", "pkill -f '[s]hanframe-broker _broker' >/dev/null 2>&1; mv "+brokerBin+".new "+brokerBin+" && chmod 755 "+brokerBin)
	cmd := fmt.Sprintf("nohup setsid %s _broker %d %s >%s 2>&1 </dev/null &", brokerBin, port, token, brokerLog)
	if out, err := adb(20*time.Second, "shell", cmd); err != nil {
		return fmt.Errorf("start broker: %v: %s", err, tail([]byte(out)))
	}
	if err := saveState(brokerState{Port: port, Token: token}); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		brokerMu.Lock()
		brokerOKAt = time.Time{}
		brokerMu.Unlock()
		if brokerAlive() {
			return nil
		}
		if time.Now().After(deadline) {
			out, _ := adb(10*time.Second, "shell", "tail -5 "+brokerLog)
			return fmt.Errorf("broker didn't come up: %s", tail([]byte(out)))
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// Ready reports whether the screen path is usable right now (broker alive).
func Ready() bool { return Available() && brokerAlive() }

// Ensure makes the screen path ready: a live broker, or — with adb reachable
// — a freshly bootstrapped one. Errors say which user step is missing.
func Ensure() error {
	if brokerAlive() {
		return nil
	}
	if !HasADB() {
		return errNoADB
	}
	if err := Connect(); err != nil {
		return err
	}
	return Bootstrap()
}

var errNoADB = errors.New("adb not installed")

// --- the broker itself

// RunBroker serves requests on 127.0.0.1:port for callers that know token.
// It is the `_broker` verb: started by Bootstrap as the shell user.
func RunBroker(port int, token string) error {
	signalIgnoreHangup()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	started := time.Now().Unix()
	log.Printf("broker: listening on %d (uid %d)", port, os.Getuid())
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go serveBroker(c, token, started)
	}
}

func signalIgnoreHangup() { signalIgnore(syscall.SIGHUP) }

func serveBroker(c net.Conn, token string, started int64) {
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(c)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return
	}
	var req brokerReq
	if json.Unmarshal(line, &req) != nil || req.Token != token {
		reply(c, brokerResp{Err: "unauthorized"})
		return
	}
	c.SetReadDeadline(time.Time{})
	switch req.Op {
	case "ping":
		reply(c, brokerResp{OK: true, Up: started, Bld: Build})
	case "exec":
		if len(req.Args) == 0 {
			reply(c, brokerResp{Err: "exec: no command"})
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, req.Args[0], req.Args[1:]...).CombinedOutput()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			reply(c, brokerResp{Err: err.Error()})
			return
		}
		reply(c, brokerResp{OK: true, Code: code})
		c.Write(out)
	case "scrcpy-start":
		pid, err := startScrcpy(req.SCID, req.Args)
		if err != nil {
			reply(c, brokerResp{Err: err.Error()})
			return
		}
		reply(c, brokerResp{OK: true, PID: pid})
	case "scrcpy-dial":
		u, err := dialAbstract("scrcpy_"+req.SCID, 8*time.Second)
		if err != nil {
			reply(c, brokerResp{Err: err.Error()})
			return
		}
		defer u.Close()
		reply(c, brokerResp{OK: true})
		done := make(chan struct{}, 2)
		go func() { io.Copy(u, r); u.(*net.UnixConn).CloseWrite(); done <- struct{}{} }()
		go func() { io.Copy(c, u); c.(*net.TCPConn).CloseWrite(); done <- struct{}{} }()
		<-done
		<-done
	default:
		reply(c, brokerResp{Err: "unknown op " + req.Op})
	}
}

func reply(w io.Writer, r brokerResp) {
	b, _ := json.Marshal(r)
	w.Write(append(b, '\n'))
}

// Build is stamped by the command so ping can report it.
var Build = "dev"

// startScrcpy launches the embedded scrcpy server as our child (shell uid,
// like us). The JAR is written next to the broker once.
func startScrcpy(scid string, opts []string) (int, error) {
	if scid == "" || strings.ContainsAny(scid, " /\\") {
		return 0, errors.New("bad scid")
	}
	if _, err := os.Stat(jarOnPhone); err != nil {
		if len(serverJar) == 0 {
			return 0, errors.New("no scrcpy server in this build")
		}
		if err := os.WriteFile(jarOnPhone+".new", serverJar, 0o644); err != nil {
			return 0, err
		}
		if err := os.Rename(jarOnPhone+".new", jarOnPhone); err != nil {
			return 0, err
		}
	}
	args := []string{"/", "com.genymobile.scrcpy.Server", scrcpyVersion,
		"scid=" + scid, "log_level=warn", "tunnel_forward=true", "cleanup=true", "send_device_meta=false", "send_dummy_byte=true"}
	args = append(args, opts...)
	cmd := exec.Command("/system/bin/app_process", args...)
	cmd.Env = append(os.Environ(), "CLASSPATH="+jarOnPhone)
	cmd.Dir = "/"
	logf, _ := os.OpenFile("/data/local/tmp/shanframe-scrcpy.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go func() {
		cmd.Wait()
		if logf != nil {
			logf.Close()
		}
	}() // reap
	return cmd.Process.Pid, nil
}

// dialAbstract connects to an abstract unix socket, retrying while the
// server is still starting.
func dialAbstract(name string, within time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(within)
	for {
		c, err := net.DialTimeout("unix", "@"+name, time.Second)
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("scrcpy server: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ParseBrokerArgs is the `_broker PORT TOKEN` argument parser.
func ParseBrokerArgs(args []string) (int, string, error) {
	if len(args) != 2 {
		return 0, "", errors.New("usage: _broker PORT TOKEN")
	}
	port, err := strconv.Atoi(args[0])
	if err != nil || port <= 0 {
		return 0, "", errors.New("bad port")
	}
	return port, args[1], nil
}

// ShellExec runs a command line as the shell user through the broker and
// returns its output and exit code (what `adb shell` would give).
func ShellExec(cmdline string) (out []byte, code int, err error) {
	c, resp, err := call(brokerReq{Op: "exec", Args: []string{"/system/bin/sh", "-c", cmdline}})
	if err != nil {
		return nil, 0, err
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	out, rerr := io.ReadAll(c)
	if rerr != nil {
		return out, 0, rerr
	}
	return out, resp.Code, nil
}
