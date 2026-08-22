package main

// Kept tunnels: `shanframe mac tunnel 9222 --install` records the forward and
// the background service keeps the local port listening from then on. The
// session to the device is opened on the first connection and dropped after
// a few idle minutes — always there for the user, nothing running when idle.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/shawnpana/shanframe/internal/peer"
	"github.com/shawnpana/shanframe/internal/rendezvous"
)

type keptTunnel struct {
	Device   string   `json:"device"`   // device name (or id) as resolved at install time
	Forwards []string `json:"forwards"` // specs: "5432", "8080:db:80", "--socks 1080"
}

func tunnelsPath() string { return filepath.Join(configDir(), "tunnels.json") }

func loadTunnels() []keptTunnel {
	var out []keptTunnel
	b, err := os.ReadFile(tunnelsPath())
	if err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

func saveTunnels(ts []keptTunnel) error {
	b, _ := json.MarshalIndent(ts, "", "  ")
	return os.WriteFile(tunnelsPath(), b, 0o600)
}

// specsOf renders forwards back to the CLI spelling kept on disk.
func specsOf(fwds []forward) []string {
	var out []string
	for _, f := range fwds {
		switch {
		case f.socks:
			out = append(out, "--socks "+strconv.Itoa(f.local))
		case f.host == "localhost" && f.local == f.remote:
			out = append(out, strconv.Itoa(f.local))
		default:
			out = append(out, fmt.Sprintf("%d:%s:%d", f.local, f.host, f.remote))
		}
	}
	return out
}

func specsToArgs(specs []string) []string {
	var args []string
	for _, s := range specs {
		if len(s) > 8 && s[:8] == "--socks " {
			args = append(args, "--socks", s[8:])
		} else {
			args = append(args, s)
		}
	}
	return args
}

const keeperIdle = 5 * time.Minute

// keeper holds one device's kept forwards and its lazily opened session.
type keeper struct {
	dev    string
	fwds   []forward
	mu     sync.Mutex
	conn   *peer.Conn
	ctl    io.Closer
	cancel func()
	active int
	last   time.Time
}

var keepersActive sync.Map // device → *keeper, for busy()

func tunnelsBusy() bool {
	busy := false
	keepersActive.Range(func(_, v any) bool {
		k := v.(*keeper)
		k.mu.Lock()
		if k.active > 0 {
			busy = true
		}
		k.mu.Unlock()
		return !busy
	})
	return busy
}

// keepTunnels starts every kept tunnel's listeners (called from serve).
func keepTunnels() {
	for _, t := range loadTunnels() {
		fwds, err := parseForwards(specsToArgs(t.Forwards))
		if err != nil {
			log.Printf("kept tunnel %s: %v", t.Device, err)
			continue
		}
		k := &keeper{dev: t.Device, fwds: fwds}
		keepersActive.Store(t.Device, k)
		for _, f := range fwds {
			ln, err := net.Listen("tcp", "localhost:"+strconv.Itoa(f.local))
			if err != nil {
				log.Printf("kept tunnel %s: listen :%d: %v", t.Device, f.local, err)
				continue
			}
			log.Printf("kept tunnel: localhost:%d → %s (%s)", f.local, t.Device, specsOf([]forward{f})[0])
			go serveForward(ln, f, k.dial, t.Device)
		}
		go k.reap()
	}
}

// dial gives serveForward a remote stream, opening the session on demand.
func (k *keeper) dial(host string, port int) (io.ReadWriteCloser, error) {
	k.mu.Lock()
	conn := k.conn
	k.mu.Unlock()
	if conn == nil {
		var err error
		if conn, err = k.open(); err != nil {
			return nil, err
		}
	}
	k.mu.Lock()
	k.active++
	k.last = time.Now()
	k.mu.Unlock()
	s, err := dialRemote(conn, host, port)
	if err != nil {
		k.done()
		if errors.Is(err, errSessionGone) {
			k.drop()
		}
		return nil, err
	}
	return &countedStream{ReadWriteCloser: s, k: k}, nil
}

type countedStream struct {
	io.ReadWriteCloser
	k    *keeper
	once sync.Once
}

func (c *countedStream) Close() error {
	c.once.Do(c.k.done)
	return c.ReadWriteCloser.Close()
}

func (k *keeper) done() {
	k.mu.Lock()
	k.active--
	k.last = time.Now()
	k.mu.Unlock()
}

var errSessionGone = errors.New("session gone")

func (k *keeper) open() (*peer.Conn, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.conn != nil {
		return k.conn, nil
	}
	c, cancel, err := newClient()
	if err != nil {
		return nil, err
	}
	dev, err := c.target(k.dev)
	if err != nil {
		cancel()
		return nil, err
	}
	ctl, conn, err := c.openConn(dev, rendezvous.Open{Service: "tunnel"})
	if err != nil {
		cancel()
		return nil, err
	}
	log.Printf("kept tunnel: connected to %s", dev.Name)
	k.conn, k.ctl, k.cancel = conn, ctl, cancel
	go func() { // session ended underneath us → forget it; next use reconnects
		io.Copy(io.Discard, ctl)
		k.mu.Lock()
		if k.conn == conn {
			k.conn = nil
			conn.Close()
			cancel()
			log.Printf("kept tunnel: %s went away", dev.Name)
		}
		k.mu.Unlock()
	}()
	return conn, nil
}

// drop closes the session; reap calls it after idle.
func (k *keeper) drop() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.conn == nil {
		return
	}
	k.ctl.Close()
	k.conn.Close()
	k.cancel()
	k.conn = nil
	log.Printf("kept tunnel: %s idle, disconnected", k.dev)
}

func (k *keeper) reap() {
	for range time.Tick(30 * time.Second) {
		k.mu.Lock()
		idle := k.conn != nil && k.active == 0 && time.Since(k.last) > keeperIdle
		k.mu.Unlock()
		if idle {
			k.drop()
		}
	}
}
