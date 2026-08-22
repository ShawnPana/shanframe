package main

// `shanframe <dev> cdp`: make the far machine's Chrome DevTools reachable as
// localhost here and print the URL to hand a CDP client. Finds the browser's
// DevToolsActivePort on the device (Chrome's gated remote-debugging mode
// writes the port and browser WebSocket path there), tunnels the port, probes
// whether /json discovery answers (classic) or not (gated), prints both forms.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shawnpana/shanframe/internal/frame"
	"github.com/shawnpana/shanframe/internal/rendezvous"
)

// devtoolsFiles is where Chromium-family browsers write DevToolsActivePort.
var devtoolsFiles = []string{
	// macOS
	"$HOME/Library/Application Support/Google/Chrome/DevToolsActivePort",
	"$HOME/Library/Application Support/Google/Chrome Canary/DevToolsActivePort",
	"$HOME/Library/Application Support/Chromium/DevToolsActivePort",
	"$HOME/Library/Application Support/BraveSoftware/Brave-Browser/DevToolsActivePort",
	"$HOME/Library/Application Support/Microsoft Edge/DevToolsActivePort",
	"$HOME/Library/Application Support/Arc/User Data/DevToolsActivePort",
	// Linux
	"$HOME/.config/google-chrome/DevToolsActivePort",
	"$HOME/.config/chromium/DevToolsActivePort",
	"$HOME/.config/BraveSoftware/Brave-Browser/DevToolsActivePort",
	"$HOME/.config/microsoft-edge/DevToolsActivePort",
	"$HOME/snap/chromium/common/chromium/DevToolsActivePort",
}

// exec runs one command on a device and returns its stdout and exit code.
func (c *client) exec(dev *rendezvous.Device, cmd string) (string, int, error) {
	s, done, err := c.open(dev, rendezvous.Open{Service: "exec", Cmd: cmd})
	if err != nil {
		return "", -1, err
	}
	defer done()
	frame.Write(s, frame.Eof, nil)
	var out bytes.Buffer
	for {
		typ, p, err := frame.Read(s)
		if err != nil {
			return out.String(), -1, err
		}
		switch typ {
		case frame.Data:
			out.Write(p)
		case frame.Error:
			return out.String(), -1, errors.New(string(p))
		case frame.Exit:
			code := 255
			if len(p) == 4 {
				code = int(p[0])<<24 | int(p[1])<<16 | int(p[2])<<8 | int(p[3])
			}
			return out.String(), code, nil
		}
	}
}

func cdp(target string, args []string) error {
	asJSON, remotePort, localPort := false, 0, 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--port":
			if i+1 < len(args) {
				remotePort, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--local":
			if i+1 < len(args) {
				localPort, _ = strconv.Atoi(args[i+1])
				i++
			}
		}
	}
	c, cancel, err := newClient()
	if err != nil {
		return err
	}
	defer cancel()
	dev, err := c.target(target)
	if err != nil {
		return err
	}

	// 1. find the browser's DevToolsActivePort (port + browser ws path)
	wsPath := ""
	if remotePort == 0 {
		var sb strings.Builder
		sb.WriteString("for f in")
		for _, f := range devtoolsFiles {
			sb.WriteString(" \"" + f + "\"")
		}
		sb.WriteString("; do [ -f \"$f\" ] && { echo \"$f\"; cat \"$f\"; exit 0; }; done; exit 1")
		out, code, err := c.exec(dev, sb.String())
		if err != nil {
			return err
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if code == 0 && len(lines) >= 2 {
			remotePort, _ = strconv.Atoi(strings.TrimSpace(lines[1]))
			if len(lines) > 2 {
				wsPath = strings.TrimSpace(lines[2])
			}
			fmt.Fprintf(os.Stderr, "cdp: %s has remote debugging on port %d (%s)\n", dev.Name, remotePort, lines[0])
		} else {
			// no DevToolsActivePort (some launches don't write one): look for a
			// Chromium-family process with a listening TCP port
			out, _, err := c.exec(dev, `(ss -ltnp 2>/dev/null | grep -iE 'chrom|brave|edge|arc' | grep -oE '127\.0\.0\.1:[0-9]+|\[::1\]:[0-9]+' ; lsof -nP -iTCP -sTCP:LISTEN 2>/dev/null | grep -iE 'chrome|chromium|brave|edge|arc' | grep -oE '127\.0\.0\.1:[0-9]+|\[::1\]:[0-9]+') | head -1`)
			if err != nil {
				return err
			}
			if i := strings.LastIndex(strings.TrimSpace(out), ":"); i > 0 {
				remotePort, _ = strconv.Atoi(strings.TrimSpace(out)[i+1:])
			}
			if remotePort == 0 {
				return fmt.Errorf("no browser with remote debugging on %s — in Chrome open chrome://inspect/#remote-debugging and allow it, or launch a Chromium with --remote-debugging-port=9222 --user-data-dir=…; or pass --port", dev.Name)
			}
			fmt.Fprintf(os.Stderr, "cdp: %s has a browser listening on port %d\n", dev.Name, remotePort)
		}
	}

	// 2. tunnel it
	if localPort == 0 {
		localPort = remotePort
	}
	ln, err := net.Listen("tcp", "localhost:"+strconv.Itoa(localPort))
	if err != nil && localPort == remotePort { // busy here (a local browser?) → any free port
		ln, err = net.Listen("tcp", "localhost:0")
	}
	if err != nil {
		return fmt.Errorf("listen: %v", err)
	}
	localPort = ln.Addr().(*net.TCPAddr).Port
	ctl, conn, err := c.openConn(dev, rendezvous.Open{Service: "tunnel"})
	if err != nil {
		return err
	}
	defer conn.Close()
	defer ctl.Close()
	go serveForward(ln, forward{local: localPort, host: "localhost", remote: remotePort},
		func(h string, p int) (io.ReadWriteCloser, error) { return dialRemote(conn, h, p) }, dev.Name)

	// 3. classic or gated?
	base := fmt.Sprintf("http://localhost:%d", localPort)
	mode, ws := "gated", ""
	if wsPath != "" {
		ws = fmt.Sprintf("ws://localhost:%d%s", localPort, wsPath)
	}
	hc := &http.Client{Timeout: 5 * time.Second}
	if resp, err := hc.Get(base + "/json/version"); err == nil {
		var v struct {
			Browser string `json:"Browser"`
			WS      string `json:"webSocketDebuggerUrl"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&v)
		resp.Body.Close()
		if resp.StatusCode == 200 && v.WS != "" {
			mode = "classic"
			ws = v.WS
			fmt.Fprintf(os.Stderr, "cdp: %s\n", v.Browser)
		}
	} else {
		return fmt.Errorf("the port is tunnelled but nothing answered on it: %v", err)
	}
	if ws == "" {
		return errors.New("browser is in gated mode but its DevToolsActivePort has no WebSocket path — reopen the browser and try again")
	}

	// 4. hand it over
	if asJSON {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"device": dev.Name, "mode": mode, "url": base, "ws": ws, "local_port": localPort})
	} else {
		fmt.Printf("CDP_WS=%s\n", ws)
		if mode == "classic" {
			fmt.Printf("CDP_URL=%s\n", base)
		}
		fmt.Fprintf(os.Stderr, "cdp: %s mode — e.g.  BU_NAME=%s BU_CDP_WS=%s browser-harness …   (Ctrl-C to stop)\n", mode, strings.ToLower(strings.Fields(dev.Name)[0]), ws)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	closed := make(chan struct{})
	go func() { io.Copy(io.Discard, ctl); close(closed) }()
	select {
	case <-sig:
	case <-closed:
		ln.Close()
		return fmt.Errorf("connection to %s closed", dev.Name)
	}
	ln.Close()
	return nil
}
