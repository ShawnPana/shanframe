package main

// tunnel (controller side): port forwards and a SOCKS5 proxy whose
// connections are dialed from the remote device. `shanframe mac tunnel 9222`
// makes the Mac's Chrome DevTools local; `--socks 1080` lends this machine the
// Mac's whole network at the proxy level (HTTP_PROXY=socks5h://localhost:1080).

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shawnpana/shanframe/internal/peer"
	"github.com/shawnpana/shanframe/internal/rendezvous"
	"github.com/shawnpana/shanframe/internal/setup"
)

type forward struct {
	local  int
	host   string
	remote int
	socks  bool
}

func parseForwards(args []string) ([]forward, error) {
	var out []forward
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--socks" {
			if i+1 >= len(args) {
				return nil, errors.New("--socks needs a port")
			}
			p, err := strconv.Atoi(args[i+1])
			if err != nil {
				return nil, fmt.Errorf("--socks: bad port %q", args[i+1])
			}
			out = append(out, forward{local: p, socks: true})
			i++
			continue
		}
		parts := strings.Split(a, ":")
		f := forward{host: "localhost"}
		var err error
		switch len(parts) {
		case 1: // port
			f.local, err = strconv.Atoi(parts[0])
			f.remote = f.local
		case 2: // lport:rport
			f.local, err = strconv.Atoi(parts[0])
			if err == nil {
				f.remote, err = strconv.Atoi(parts[1])
			}
		case 3: // lport:host:rport
			f.local, err = strconv.Atoi(parts[0])
			f.host = parts[1]
			if err == nil {
				f.remote, err = strconv.Atoi(parts[2])
			}
		default:
			err = errors.New("expected port, lport:rport or lport:host:rport")
		}
		if err != nil {
			return nil, fmt.Errorf("tunnel %q: %v", a, err)
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, errors.New("nothing to forward")
	}
	return out, nil
}

func tunnel(target string, args []string) error {
	mode := ""
	var rest []string
	for _, a := range args {
		if a == "--install" || a == "--uninstall" {
			mode = a
		} else {
			rest = append(rest, a)
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
	if mode == "--uninstall" {
		var keep []keptTunnel
		for _, t := range loadTunnels() {
			if t.Device != dev.Name && t.Device != dev.ID {
				keep = append(keep, t)
			}
		}
		if err := saveTunnels(keep); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "removed kept tunnels to %s\n", dev.Name)
		return setup.RestartService()
	}
	fwds, err := parseForwards(rest)
	if err != nil {
		return err
	}
	if mode == "--install" {
		ts := loadTunnels()
		found := false
		for i := range ts {
			if ts[i].Device == dev.Name || ts[i].Device == dev.ID {
				ts[i].Device = dev.Name
				ts[i].Forwards = specsOf(fwds)
				found = true
			}
		}
		if !found {
			ts = append(ts, keptTunnel{Device: dev.Name, Forwards: specsOf(fwds)})
		}
		if err := saveTunnels(ts); err != nil {
			return err
		}
		for _, f := range fwds {
			if f.socks {
				fmt.Fprintf(os.Stderr, "kept: socks5://localhost:%d → out through %s\n", f.local, dev.Name)
			} else {
				fmt.Fprintf(os.Stderr, "kept: localhost:%d → %s:%s:%d\n", f.local, dev.Name, f.host, f.remote)
			}
		}
		fmt.Fprintln(os.Stderr, "always on from now; connects to the device when something uses it")
		return setup.RestartService()
	}
	ctl, conn, err := c.openConn(dev, rendezvous.Open{Service: "tunnel"})
	if err != nil {
		return err
	}
	defer conn.Close()
	defer ctl.Close()

	var listeners []net.Listener
	for _, f := range fwds {
		ln, err := net.Listen("tcp", "localhost:"+strconv.Itoa(f.local))
		if err != nil {
			return fmt.Errorf("listen on :%d: %v", f.local, err)
		}
		listeners = append(listeners, ln)
		if f.socks {
			fmt.Fprintf(os.Stderr, "tunnel: socks5://localhost:%d → out through %s\n", f.local, dev.Name)
		} else {
			fmt.Fprintf(os.Stderr, "tunnel: localhost:%d → %s:%s:%d\n", f.local, dev.Name, f.host, f.remote)
		}
		go serveForward(ln, f, func(h string, p int) (io.ReadWriteCloser, error) { return dialRemote(conn, h, p) }, dev.Name)
	}
	fmt.Fprintln(os.Stderr, "tunnel: ready (Ctrl-C to stop)")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	closed := make(chan struct{})
	go func() { io.Copy(io.Discard, ctl); close(closed) }() // control stream ends when the session does
	select {
	case <-sig:
	case <-closed:
		for _, ln := range listeners {
			ln.Close()
		}
		return fmt.Errorf("connection to %s closed", dev.Name)
	}
	for _, ln := range listeners {
		ln.Close()
	}
	return nil
}

func serveForward(ln net.Listener, f forward, dial func(host string, port int) (io.ReadWriteCloser, error), devName string) {
	for {
		local, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer local.Close()
			host, port := f.host, f.remote
			if f.socks {
				var err error
				if host, port, err = socksHandshake(local); err != nil {
					return
				}
			}
			remote, err := dial(host, port)
			if f.socks {
				socksReply(local, err == nil)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "tunnel: %s:%d via %s: %v\n", host, port, devName, err)
				return
			}
			pipe(local, remote)
		}()
	}
}

// dialRemote opens a tcp stream on the session and waits for the device's
// status byte: 0 = connected, 1 = failed (+ reason).
func dialRemote(conn *peer.Conn, host string, port int) (io.ReadWriteCloser, error) {
	s, err := conn.Dial(rendezvous.Open{Service: "tcp", Host: host, Port: port}, 15*time.Second)
	if err != nil {
		return nil, err
	}
	var st [1]byte
	if _, err := io.ReadFull(s, st[:]); err != nil {
		s.Close()
		return nil, err
	}
	if st[0] != 0 {
		buf := make([]byte, 256)
		n, _ := s.Read(buf)
		s.Close()
		return nil, errors.New(strings.TrimSpace(string(buf[:n])))
	}
	return s, nil
}

// socksHandshake does the no-auth SOCKS5 greeting + CONNECT request and
// returns the requested host (kept as a name so it resolves remotely) and port.
func socksHandshake(c net.Conn) (string, int, error) {
	c.SetDeadline(time.Now().Add(10 * time.Second))
	defer c.SetDeadline(time.Time{})
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil || hdr[0] != 5 {
		return "", 0, errors.New("not socks5")
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", 0, err
	}
	c.Write([]byte{5, 0}) // no auth
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil {
		return "", 0, err
	}
	if req[1] != 1 { // CONNECT only
		c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return "", 0, errors.New("socks: only CONNECT is supported")
	}
	var host string
	switch req[3] {
	case 1:
		var a [4]byte
		io.ReadFull(c, a[:])
		host = net.IP(a[:]).String()
	case 3:
		var n [1]byte
		io.ReadFull(c, n[:])
		b := make([]byte, int(n[0]))
		io.ReadFull(c, b)
		host = string(b)
	case 4:
		var a [16]byte
		io.ReadFull(c, a[:])
		host = net.IP(a[:]).String()
	default:
		c.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0})
		return "", 0, errors.New("socks: bad address type")
	}
	var p [2]byte
	if _, err := io.ReadFull(c, p[:]); err != nil {
		return "", 0, err
	}
	return host, int(binary.BigEndian.Uint16(p[:])), nil
}

func socksReply(c net.Conn, ok bool) {
	rep := byte(5) // connection refused
	if ok {
		rep = 0
	}
	c.Write([]byte{5, rep, 0, 1, 0, 0, 0, 0, 0, 0})
}
