package android

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LookPath finds a program on PATH the way exec.LookPath does, without the
// faccessat2 syscall: Android's seccomp policy for apps kills a process that
// makes it (SIGSYS), and Go's exec.LookPath uses it. Safe everywhere.
func LookPath(name string) (string, error) {
	if strings.Contains(name, "/") {
		if isExecutable(name) {
			return name, nil
		}
		return "", errors.New(name + ": not executable")
	}
	dirs := filepath.SplitList(os.Getenv("PATH"))
	dirs = append(dirs, "/data/data/com.termux/files/usr/bin", "/system/bin")
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if p := filepath.Join(d, name); isExecutable(p) {
			return p, nil
		}
	}
	return "", errors.New(name + ": not found")
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// Command is exec.Command with the program resolved by LookPath first, so it
// never trips Android's seccomp filter. Use it for anything that can run on
// a phone.
func Command(name string, args ...string) *exec.Cmd {
	if p, err := LookPath(name); err == nil {
		name = p
	}
	return exec.Command(name, args...)
}
