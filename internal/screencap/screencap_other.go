//go:build !darwin || !cgo

package screencap

import "errors"

type Frame struct {
	Data  []byte
	Key   bool
	PTSMs int64
}

type Session struct{ W, H int }

func Supported() bool             { return false }
func Authorized() bool            { return false }
func RequestPermission() bool     { return false }
func (s *Session) Stop()          {}
func (s *Session) ForceKeyframe() {}
func Start(maxDim, fps, bitrate int, cb func(Frame)) (*Session, error) {
	return nil, errors.New("native screen capture is not available on this platform")
}

// Cursor is unavailable without native capture.
func Still() ([]byte, int, int, error) {
	return nil, 0, 0, errors.New("no screen capture on this device")
}

func Cursor() ([]byte, float64, float64, float64, float64, bool) {
	return nil, 0, 0, 0, 0, false
}
