//go:build !darwin && !linux

package setup

func RecentLog(logPath string, n int) string { return "" }
