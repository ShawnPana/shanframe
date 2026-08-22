package setup

import (
	"os"
	"strings"
)

// RecentLog returns the last n lines the service wrote (launchd sends
// stdout/stderr to logPath).
func RecentLog(logPath string, n int) string {
	b, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
