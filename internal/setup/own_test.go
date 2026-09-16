package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnBinary(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	// writable home: nothing to do
	exe := filepath.Join(root, "bin", "shanframe")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("#!/bin/sh\necho agent\n"), 0o755)
	got, moved, err := OwnBinary(exe, state)
	if err != nil || moved || got != exe {
		t.Fatalf("writable dir: got %q moved=%v err=%v", got, moved, err)
	}
	// unwritable home (like /usr/local/bin for a non-root service user)
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	os.Chmod(filepath.Dir(exe), 0o555)
	t.Cleanup(func() { os.Chmod(filepath.Dir(exe), 0o755) })
	got, moved, err = OwnBinary(exe, state)
	if err != nil || !moved || got != filepath.Join(state, "bin", "shanframe") {
		t.Fatalf("unwritable dir: got %q moved=%v err=%v", got, moved, err)
	}
	b, err := os.ReadFile(got)
	if err != nil || string(b) != "#!/bin/sh\necho agent\n" {
		t.Fatalf("copy: %v %q", err, b)
	}
	if st, _ := os.Stat(got); st.Mode()&0o111 == 0 {
		t.Fatal("copy not executable")
	}
	if _, err := os.Stat(got + ".new"); err == nil {
		t.Fatal("temp file left behind")
	}
}
