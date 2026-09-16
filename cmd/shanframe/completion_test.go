package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// complete runs cobra's completion protocol for a typed line (the last word
// is the one being completed) and returns the offered words.
func complete(t *testing.T, words ...string) []string {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs(append([]string{cobra.ShellCompRequestCmd}, words...))
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range strings.Split(out.String(), "\n") {
		if l != "" && !strings.HasPrefix(l, ":") {
			got = append(got, l)
		}
	}
	return got
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestCompletion(t *testing.T) {
	t.Setenv("SHANFRAME_DIR", t.TempDir()) // no config: device names come from the (empty) cache, no network
	got := complete(t, "")
	for _, verb := range []string{"ls", "join", "up", "down", "rm", "startcmd", "tunnels", "completion"} {
		if !has(got, verb) {
			t.Errorf("top level lacks %q: %q", verb, got)
		}
	}
	if has(got, "_screencap") || has(got, "_broker") {
		t.Error("hidden verb offered")
	}
	if !has(got, "pair") {
		t.Error("pair not offered")
	}
	got = complete(t, "Raspberry Pi 5", "")
	for _, a := range []string{"run", "tunnel", "cdp", "startcmd", "screenshot", "click", "dblclick", "key", "batch"} {
		if !has(got, a) {
			t.Errorf("device actions lack %q: %q", a, got)
		}
	}
	for verb := range screenVerbs {
		if !has(got, verb) {
			t.Errorf("screen verb %q not completed", verb)
		}
	}
	if got = complete(t, "zulu", "tunnel", "--"); !has(got, "--socks") || !has(got, "--install") {
		t.Errorf("tunnel flags: %q", got)
	}
	if got = complete(t, "zulu", "run", ""); len(got) != 0 {
		t.Errorf("nothing should follow run: %q", got)
	}
	if got = complete(t, "completion", ""); !has(got, "zsh") || !has(got, "install") {
		t.Errorf("completion sub verbs: %q", got)
	}
}

func TestDeviceNamesCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHANFRAME_DIR", dir)
	os.WriteFile(filepath.Join(dir, "devices.cache"), []byte("Raspberry Pi 5\nShawn’s MacBook Pro\n"), 0o600)
	got := complete(t, "")
	if !has(got, "Raspberry Pi 5") || !has(got, "Shawn’s MacBook Pro") {
		t.Errorf("cached device names not offered: %q", got)
	}
	if got = complete(t, "rm", ""); !has(got, "Raspberry Pi 5") {
		t.Errorf("rm should offer devices: %q", got)
	}
}

func TestSetMarkedLine(t *testing.T) {
	rc := filepath.Join(t.TempDir(), "rc")
	read := func() string { b, _ := os.ReadFile(rc); return string(b) }
	if err := setMarkedLine(rc, "# m", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rc); err == nil {
		t.Fatal("remove created the file")
	}
	if err := setMarkedLine(rc, "# m", "one # m"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "one # m\n" {
		t.Fatalf("after add: %q", got)
	}
	os.WriteFile(rc, []byte("export X=1\none # m\nalias y=z\n"), 0o644)
	if err := setMarkedLine(rc, "# m", "two # m"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "export X=1\nalias y=z\ntwo # m\n" {
		t.Fatalf("after replace: %q", got)
	}
	if err := setMarkedLine(rc, "# m", ""); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "export X=1\nalias y=z\n" {
		t.Fatalf("after remove: %q", got)
	}
}

func TestInstallCompletionRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHANFRAME_DIR", filepath.Join(home, "cfg"))
	t.Setenv("SHELL", "/bin/zsh")
	root := newRootCmd()
	note, err := installCompletion(root)
	if err != nil || note == "" {
		t.Fatalf("install: %v (note %q)", err, note)
	}
	script, err := os.ReadFile(filepath.Join(home, "cfg", "completion.zsh"))
	if err != nil || !strings.Contains(string(script), "compdef _shanframe shanframe") {
		t.Fatalf("zsh script: %v %q", err, string(script)[:min(len(script), 80)])
	}
	if _, err = installCompletion(root); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".zshrc"))
	if n := strings.Count(string(b), completionMarker); n != 1 {
		t.Fatalf("two installs left %d marked lines: %q", n, b)
	}
	if err := uninstallCompletion(); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(home, ".zshrc"))
	if strings.Contains(string(b), completionMarker) {
		t.Fatalf("uninstall left: %q", b)
	}
	if _, err := os.Stat(filepath.Join(home, "cfg", "completion.zsh")); err == nil {
		t.Fatal("script not removed")
	}
	t.Setenv("SHELL", "/usr/bin/fish")
	if note, err = installCompletion(root); err != nil || note == "" {
		t.Fatalf("fish: %q %v", note, err)
	}
	if _, err := os.Stat(fishCompletionPath(home)); err != nil {
		t.Fatal("fish completion not written")
	}
	t.Setenv("SHELL", "/bin/nushell")
	if note, err = installCompletion(root); err != nil || note != "" {
		t.Fatalf("unsupported shell should be a quiet no-op: %q %v", note, err)
	}
}
