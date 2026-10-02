//go:build unix

package update

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// A descriptor opened without close-on-exec (what a system framework hands
// us) must not survive into children or into a re-exec once the sweep ran.
func TestMarkCloseOnExecKeepsStrayDescriptorsOutOfChildren(t *testing.T) {
	var p [2]int
	if err := syscall.Pipe(p[:]); err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(p[0])
	defer syscall.Close(p[1])
	// clear the flag, as a framework that never set it would leave it
	for _, fd := range p {
		if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, 0); e != 0 {
			t.Fatal(e)
		}
	}
	sees := func() bool {
		out, err := exec.Command("/bin/sh", "-c", "test -e /dev/fd/"+strconv.Itoa(p[0])+" && echo leaked || echo clean").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out)) == "leaked"
	}
	if !sees() {
		t.Skip("this platform doesn't pass stray descriptors to children; nothing to protect against")
	}
	if n := MarkCloseOnExec(); n < 5 { // stdio + the pipe at least
		t.Fatalf("counted %d descriptors, expected at least 5", n)
	}
	if sees() {
		t.Fatal("after the sweep a child still sees the stray descriptor")
	}
	flags, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(p[1]), syscall.F_GETFD, 0)
	if e != 0 || flags&syscall.FD_CLOEXEC == 0 {
		t.Fatalf("FD_CLOEXEC not set on the write end: flags=%d err=%v", flags, e)
	}
	// stdio stays inheritable: children need a terminal
	flags, _, _ = syscall.Syscall(syscall.SYS_FCNTL, 1, syscall.F_GETFD, 0)
	if flags&syscall.FD_CLOEXEC != 0 {
		t.Fatal("stdout must not be marked close-on-exec")
	}
}
