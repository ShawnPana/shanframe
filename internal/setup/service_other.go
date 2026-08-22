//go:build !linux && !darwin

package setup

import "errors"

func InstallService(exe, logPath string) error {
	return errors.New("not supported on this OS yet")
}
func UninstallService() error       { return errors.New("not supported on this OS yet") }
func ServiceDescription() string    { return "unsupported" }
func LogHint(logPath string) string { return logPath }

func RestartService() error { return errors.New("not supported on this OS yet") }
