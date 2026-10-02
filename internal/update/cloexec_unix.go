//go:build unix

package update

import (
	"os"
	"strconv"
	"syscall"
)

// MarkCloseOnExec sets FD_CLOEXEC on every descriptor above stderr and
// returns how many descriptors the process holds. Go marks its own
// descriptors close-on-exec, but the system frameworks the agent calls into
// (the macOS permission prompt opens a kernel-control socket, for one) do
// not, so without this each in-place re-exec carried one more descriptor
// over, and every shell or command the agent started inherited the pile —
// a farm Mac that never got Screen Recording re-exec'd every two minutes
// until its shells failed with "too many open files". Marking, not closing:
// the framework may still be using the descriptor; it just stays out of
// the next image and out of children.
func MarkCloseOnExec() int {
	dir, err := os.Open("/dev/fd")
	if err != nil {
		return 0
	}
	names, _ := dir.Readdirnames(-1)
	self := int(dir.Fd())
	dir.Close()
	n := 0
	for _, name := range names {
		fd, err := strconv.Atoi(name)
		if err != nil || fd == self {
			continue
		}
		n++
		if fd <= 2 {
			continue
		}
		syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, syscall.FD_CLOEXEC)
	}
	return n
}
