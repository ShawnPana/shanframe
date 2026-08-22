package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLastCrash(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHANFRAME_DIR", dir)
	log := filepath.Join(dir, "serve.log")
	write := func(s string) { os.WriteFile(log, []byte(s), 0o644) }

	write("2026/08/22 01:00:00 shanframe \"mac\" (build aaa) → https://x\n2026/08/22 01:00:01 connected\npanic: boom\n\ngoroutine 1 [running]:\nmain.serve()\n")
	if got := lastCrash(); !strings.Contains(got, "panic: boom") || strings.Contains(got, "shanframe \"mac\"") == false {
		t.Fatalf("expected crash segment, got %q", got)
	}
	write("2026/08/22 01:00:00 shanframe \"mac\" (build aaa) → https://x\npanic: old\n2026/08/22 02:00:00 shanframe \"mac\" (build bbb) → https://x\nconnected\n")
	if got := lastCrash(); got != "" {
		t.Fatalf("old crash must not be re-reported, got %q", got)
	}
	os.Remove(log)
	if got := lastCrash(); got != "" {
		t.Fatalf("no log → no report, got %q", got)
	}
}
