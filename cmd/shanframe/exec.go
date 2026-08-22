package main

// exec service (agent side): run one command line in the user's login shell
// without a pty — stdout/stderr kept apart, exit code returned — so agents
// and scripts get ssh-like semantics: `shanframe pi run -- make test`.

import (
	"encoding/binary"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/shawnpana/shanframe/internal/frame"
	"github.com/shawnpana/shanframe/internal/ptyx"
)

func serveExec(s io.ReadWriteCloser, cmdline string) {
	if cmdline == "" {
		frame.Write(s, frame.Error, []byte("exec: no command"))
		return
	}
	log.Printf("exec → %q", cmdline)
	cmd := exec.Command(ptyx.Shell(), "-lc", cmdline)
	cmd.Env = append(os.Environ(), "SHANFRAME=1")
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // kill the whole tree when the client goes away
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		frame.Write(s, frame.Error, []byte("exec: "+err.Error()))
		return
	}
	var wmu sync.Mutex
	write := func(typ byte, p []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		frame.Write(s, typ, p)
	}
	var wg sync.WaitGroup
	pump := func(r io.Reader, typ byte) {
		defer wg.Done()
		buf := make([]byte, 32<<10)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				write(typ, buf[:n])
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pump(stdout, frame.Data)
	go pump(stderr, frame.Stderr)
	go func() { // client → process stdin; a dead stream kills the process group
		for {
			typ, p, err := frame.Read(s)
			if err != nil {
				stdin.Close()
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				return
			}
			switch typ {
			case frame.Data:
				stdin.Write(p)
			case frame.Eof:
				stdin.Close()
			}
		}
	}()
	wg.Wait()
	err := cmd.Wait()
	code := 0
	if err != nil {
		code = 255
		if ee, ok := err.(*exec.ExitError); ok {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			} else {
				code = ee.ExitCode()
			}
		}
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(code))
	write(frame.Exit, b[:])
	log.Printf("exec ← exit %d", code)
}
