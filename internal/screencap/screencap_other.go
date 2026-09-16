//go:build !darwin || !cgo

package screencap

// Without native capture of its own, a platform has a screen only when it is
// an Android phone: frames come from the phone's own encoder through its
// debugging channel (internal/android).

import (
	"errors"

	"github.com/shawnpana/shanframe/internal/android"
)

// Frame is one encoded H.264 access unit (Annex-B, SPS/PPS on keyframes).
type Frame struct {
	Data  []byte
	Key   bool
	PTSMs int64
}

// Session is a running capture of the display.
type Session struct {
	W, H int
	s    *android.Stream
}

// Supported reports whether this device can capture its screen.
func Supported() bool { return android.Available() }

// Authorized reports whether the capture channel is usable right now.
func Authorized() bool { return android.Ready() }

// RequestPermission has nothing to ask for here; the pairing step is the
// permission and setup reports it.
func RequestPermission() bool { return false }

// Start captures the display, delivering encoded frames to cb from a reader
// goroutine. maxDim caps the long side in pixels.
func Start(maxDim, fps, bitrate int, cb func(Frame)) (*Session, error) {
	if !android.Available() {
		return nil, errors.New("native screen capture is not available on this platform")
	}
	s, err := android.StartStream(maxDim, fps, bitrate, func(data []byte, key bool, ptsMs int64) {
		cb(Frame{Data: data, Key: key, PTSMs: ptsMs})
	})
	if err != nil {
		return nil, err
	}
	w, h := s.Size()
	return &Session{W: w, H: h, s: s}, nil
}

// ForceKeyframe makes the next encoded frame a keyframe (viewer recovery).
func (s *Session) ForceKeyframe() {
	if s != nil && s.s != nil {
		s.s.ForceKeyframe()
	}
}

// Stop ends the capture; no callbacks after it returns.
func (s *Session) Stop() {
	if s != nil && s.s != nil {
		s.s.Stop()
	}
}

// Still grabs one PNG of the display with its pixel size.
func Still() ([]byte, int, int, error) {
	if !android.Available() {
		return nil, 0, 0, errors.New("no screen capture on this device")
	}
	return android.Screenshot()
}

// Cursor is unavailable: a phone draws no pointer.
func Cursor() ([]byte, float64, float64, float64, float64, bool) {
	return nil, 0, 0, 0, 0, false
}
