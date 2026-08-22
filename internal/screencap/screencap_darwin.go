//go:build darwin && cgo

// Package screencap captures the screen as H.264 (Annex-B) frames — the
// native replacement for proxying a VNC server. macOS: ScreenCaptureKit +
// VideoToolbox. Other platforms report Supported() == false until they get
// their own capture (Linux: PipeWire, on the roadmap).
package screencap

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework ScreenCaptureKit -framework VideoToolbox -framework CoreMedia -framework CoreVideo -framework CoreGraphics
#include <stdlib.h>
#include <stdint.h>
int sfPreflight(void);
int sfRequest(void);
void *sfStart(long goID, int maxDim, int fps, int bitrate, int *outW, int *outH);
void sfStop(void *handle);
void sfForceKey(void *handle);
*/
import "C"

import (
	"errors"
	"sync"
	"unsafe"
)

// Frame is one encoded H.264 access unit (Annex-B, SPS/PPS on keyframes).
type Frame struct {
	Data  []byte
	Key   bool
	PTSMs int64
}

// Session is a running capture of the main display.
type Session struct {
	W, H   int
	handle unsafe.Pointer
	id     C.long
}

var (
	mu       sync.Mutex
	nextID   C.long
	sessions = map[C.long]func(Frame){}
)

//export shanframeFrame
func shanframeFrame(id C.long, buf *C.uint8_t, n C.int, key C.int, ptsMs C.int64_t) {
	mu.Lock()
	cb := sessions[id]
	mu.Unlock()
	if cb == nil {
		return
	}
	data := C.GoBytes(unsafe.Pointer(buf), n)
	cb(Frame{Data: data, Key: key != 0, PTSMs: int64(ptsMs)})
}

// Supported reports whether native capture exists on this platform.
func Supported() bool { return true }

// Authorized reports whether Screen Recording permission is granted.
func Authorized() bool { return C.sfPreflight() != 0 }

// RequestPermission asks macOS to show the Screen Recording prompt (once);
// returns whether access is (now) granted.
func RequestPermission() bool { return C.sfRequest() != 0 }

// Start captures the main display, delivering encoded frames to cb from a
// capture thread. maxDim caps the long side in pixels.
func Start(maxDim, fps, bitrate int, cb func(Frame)) (*Session, error) {
	mu.Lock()
	nextID++
	id := nextID
	sessions[id] = cb
	mu.Unlock()

	var w, h C.int
	handle := C.sfStart(id, C.int(maxDim), C.int(fps), C.int(bitrate), &w, &h)
	if handle == nil {
		mu.Lock()
		delete(sessions, id)
		mu.Unlock()
		return nil, errors.New("screen capture failed to start (is Screen Recording allowed for shanframe?)")
	}
	return &Session{W: int(w), H: int(h), handle: handle, id: id}, nil
}

// ForceKeyframe makes the next encoded frame a keyframe (viewer recovery).
func (s *Session) ForceKeyframe() {
	if s.handle != nil {
		C.sfForceKey(s.handle)
	}
}

// Stop ends the capture; no callbacks after it returns.
func (s *Session) Stop() {
	if s.handle == nil {
		return
	}
	C.sfStop(s.handle)
	s.handle = nil
	mu.Lock()
	delete(sessions, s.id)
	mu.Unlock()
}
