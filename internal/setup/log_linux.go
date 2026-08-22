package setup

import (
	"context"
	"fmt"
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
	out, _ := exec.CommandContext(ctx, "journalctl", args...).Output()
	return string(out)
}
