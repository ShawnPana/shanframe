// Package update keeps every device on the newest agent by itself: the server
// hosts one binary per os/arch; the agent compares the server's sha256 with
// its own executable and, when they differ, downloads, verifies, swaps the
// file in place and re-execs. No versions to bump, no redeploys by hand.
//
// Safety: a broken build must never strand a device (a torn binary once
// segfault-looped the Pi with nothing left to self-heal). Three layers:
//   - probe: before installing, the OLD binary runs the new one with __probe
//     and refuses it unless it starts and exits cleanly
//   - keep the previous binary beside the new one (<exe>.prev)
//   - Guard: at serve start, a build that keeps dying before it can confirm
//     itself healthy (3 strikes) is swapped back for .prev, and its sha is
//     remembered (<exe>.bad) so the updater won't flap onto it again
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProbeArg is the hidden argument the binary must answer by exiting 0 —
// proof it loads and the runtime boots. main() handles it first thing.
const ProbeArg = "__probe"

func selfExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// probe runs a candidate binary and reports whether it starts at all.
func probe(path string) error {
	cmd := exec.Command(path, ProbeArg)
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		return fmt.Errorf("probe timed out")
	}
}

func badMemo(exe string) string {
	b, _ := os.ReadFile(exe + ".bad")
	return strings.TrimSpace(string(b))
}

// Name is the artifact name for this platform, e.g. "shanframe-linux-arm64".
func Name(bin string) string { return fmt.Sprintf("%s-%s-%s", bin, runtime.GOOS, runtime.GOARCH) }

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Check asks the server for the current sha of our artifact; "" if it has none.
func Check(server, token, bin string) (string, error) {
	req, _ := http.NewRequest("GET", server+"/v1/bin/"+Name(bin)+".sha256", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("server said %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	return strings.TrimSpace(string(b)), err
}

// Apply downloads the artifact, verifies it against want, replaces our own
// executable and re-execs into it. Only returns on failure.
func Apply(server, token, bin, want string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	req, _ := http.NewRequest("GET", server+"/v1/bin/"+Name(bin), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download: %s", resp.Status)
	}
	tmp := exe + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(tmp)
		return fmt.Errorf("checksum mismatch (got %s want %s)", got[:12], want[:12])
	}
	if err := probe(tmp); err != nil {
		os.Remove(tmp)
		os.WriteFile(exe+".bad", []byte(want+"\n"), 0o644) // don't retry this build
		return fmt.Errorf("new binary failed preflight (%v) — refusing it", err)
	}
	os.Remove(exe + ".prev")
	if err := os.Rename(exe, exe+".prev"); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Rename(exe+".prev", exe) // put the world back
		os.Remove(tmp)
		return err
	}
	os.Remove(exe + ".bad")
	os.WriteFile(exe+".pending", []byte("0\n"), 0o644) // Guard confirms or reverts
	log.Printf("update: installed new %s, restarting", bin)
	relaunch(exe)
	return nil
}

// relaunch brings the new binary up. On macOS the service manager must do it:
// TCC applies Screen Recording/Accessibility grants at process launch, and a
// re-exec'd process keeps the old image's (un)authorization — after one
// self-update the screen went "not ready" until a fresh launchd start. Exiting
// lets launchd/systemd (KeepAlive / Restart=always) start a clean process.
// Elsewhere exec is fine and faster.
func relaunch(exe string) {
	if runtime.GOOS == "darwin" {
		log.Printf("update: exiting for a fresh launch (macOS applies permissions at process start)")
		os.Exit(0)
	}
	syscall.Exec(exe, os.Args, os.Environ())
	os.Exit(0) // exec failed; let the service manager recover
}

// Guard runs first thing in serve. A freshly installed build must stay alive
// for confirmAfter to count as good; one that keeps dying young (3 starts
// without confirming) is rolled back to the kept previous binary.
func Guard(confirmAfter time.Duration) {
	exe, err := selfExe()
	if err != nil {
		return
	}
	pending := exe + ".pending"
	b, err := os.ReadFile(pending)
	if err != nil {
		return // nothing to confirm
	}
	strikes, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if strikes >= 3 {
		prev := exe + ".prev"
		if _, err := os.Stat(prev); err != nil {
			os.Remove(pending) // nothing to go back to; stop counting
			return
		}
		sha, _ := fileSHA(exe)
		os.WriteFile(exe+".bad", []byte(sha+"\n"), 0o644)
		if err := os.Rename(prev, exe); err != nil {
			log.Printf("update: rollback failed: %v", err)
			return
		}
		os.Remove(pending)
		log.Printf("update: this build kept dying (3 starts) — rolled back to the previous binary (%s marked bad)", sha[:8])
		relaunch(exe)
		return
	}
	os.WriteFile(pending, []byte(strconv.Itoa(strikes+1)+"\n"), 0o644)
	go func() {
		time.Sleep(confirmAfter)
		os.Remove(pending) // survived: this build is the new good
	}()
}

// Restart re-execs the current binary in place (same pid, launchd unaware).
// Confirm marks the running build good right now. Called when the process is
// asked to stop (a signal, an install/restart) during the probation window:
// a deliberate stop is not a crash and must not count toward a rollback.
func Confirm() {
	if exe, err := selfExe(); err == nil {
		os.Remove(exe + ".pending")
	}
}

func Restart() {
	Confirm()
	exe, err := os.Executable()
	if err != nil {
		return
	}
	syscall.Exec(exe, os.Args, os.Environ())
}

// Loop checks every interval and applies updates. Blocks. busy reports
// whether live sessions would be cut by a restart; updates wait for quiet.
func Loop(server, token, bin string, interval time.Duration, busy func() bool) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)
	var waitingSince time.Time
	for {
		wait := interval
		mine, err := fileSHA(exe)
		if err == nil {
			want, err := Check(server, token, bin)
			if err != nil { // first dials to a sleepy LAN peer fail on ARP; try once more
				time.Sleep(2 * time.Second)
				want, err = Check(server, token, bin)
			}
			if err != nil {
				log.Printf("update: check: %v", err)
				wait = 30 * time.Second // transient network trouble; try again soon
			} else if want != "" && want == badMemo(exe) {
				// a build we refused or rolled back from; wait for a different one
			} else if want != "" && want != mine {
				if waitingSince.IsZero() {
					waitingSince = time.Now()
				}
				// An always-connected user never goes quiet — don't starve.
				// After 10 minutes, update anyway: sessions reconnect in ~2s.
				if busy != nil && busy() && time.Since(waitingSince) < 10*time.Minute {
					log.Printf("update: %s available (%s → %s); waiting for sessions to end", bin, mine[:8], want[:8])
					wait = 30 * time.Second
				} else {
					log.Printf("update: server has a newer %s (%s → %s)", bin, mine[:8], want[:8])
					if err := Apply(server, token, bin, want); err != nil {
						log.Printf("update: %v", err)
					}
					waitingSince = time.Time{}
				}
			}
		}
		time.Sleep(wait)
	}
}
