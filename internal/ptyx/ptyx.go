// Package ptyx spawns a login shell in a pty and bridges it to a frame stream.
package ptyx

import (
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/creack/pty"
	"github.com/shawnpana/shanframe/internal/frame"
)

// Shell is the user's login shell (what `shell` and `exec` sessions run).
func Shell() string { return shell() }

func shell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	for _, s := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return "/bin/sh"
}

// Serve runs a shell in a pty and bridges it over rw using the frame protocol
// until the shell exits or rw closes. cols/rows are the initial size.
// startCmd, when set, is typed into the pty by this side as soon as the shell
// has drawn its prompt — before any remote input can arrive — so the device
// enforces its own startup command no matter which client connects.
func Serve(rw io.ReadWriter, cols, rows int, startCmd string) error {
	cmd := exec.Command(shell(), "-l")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "SHANFRAME=1")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return err
	}
	defer f.Close()
	defer cmd.Process.Kill()

	done := make(chan struct{})
	started := startCmd == ""
	go func() { // pty → peer
		buf := make([]byte, 32*1024)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				if werr := frame.Write(rw, frame.Data, buf[:n]); werr != nil {
					break
				}
				if !started {
					started = true
					go func() { time.Sleep(150 * time.Millisecond); f.WriteString(startCmd + "\r") }()
				}
			}
			if err != nil {
				break
			}
		}
		close(done)
	}()
	go func() { // peer → pty
		for {
			typ, p, err := frame.Read(rw)
			if err != nil {
				cmd.Process.Kill()
				return
			}
			switch typ {
			case frame.Data:
				f.Write(p)
			case frame.Resize:
				if c, r, ok := frame.ParseResize(p); ok {
					pty.Setsize(f, &pty.Winsize{Cols: uint16(c), Rows: uint16(r)})
				}
			}
		}
	}()
	<-done
	return cmd.Wait()
}
