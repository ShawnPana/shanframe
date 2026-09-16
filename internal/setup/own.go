package setup

import (
	"io"
	"os"
	"path/filepath"

	"github.com/shawnpana/shanframe/internal/update"
)

// OwnBinary makes sure the agent runs from a directory its own user can
// write, so it can update itself (the updater writes the new build next to
// the running one and renames). install.sh puts the binary in /usr/local/bin
// with sudo while the service runs as the user — that left agents unable to
// update, silently. When exe sits somewhere unwritable it is copied to
// <stateDir>/bin/<name> and that path is returned with moved=true; the
// caller decides what to do with the original (a symlink keeps the command
// on PATH current).
func OwnBinary(exe, stateDir string) (path string, moved bool, err error) {
	if update.DirWritable(filepath.Dir(exe)) {
		return exe, false, nil
	}
	dest := filepath.Join(stateDir, "bin", filepath.Base(exe))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", false, err
	}
	// write beside, then rename: never a torn binary at dest
	if err := copyFile(exe, dest+".new", 0o755); err != nil {
		return "", false, err
	}
	if err := os.Rename(dest+".new", dest); err != nil {
		os.Remove(dest + ".new")
		return "", false, err
	}
	return dest, true, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
