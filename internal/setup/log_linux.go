package setup

import (
	"context"
	"fmt"
	"github.com/shawnpana/shanframe/internal/android"
	"os/exec"
	"time"
)

// RecentLog returns the last n lines of the service's journal.
func RecentLog(logPath string, n int) string {
	args := []string{"-u", "shanframe", "-n", fmt.Sprint(n), "--no-pager", "-o", "cat"}
	if installedMode() == "user" {
		args = append([]string{"--user"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if android.Available() { // no journal on a phone (and exec.LookPath would be fatal there)
		return ""
	}
	out, _ := exec.CommandContext(ctx, "journalctl", args...).Output()
	return string(out)
}
