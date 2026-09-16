package android

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"sync"
	"time"
)

// capture is the one live H.264 capture of the display (a scrcpy server
// with video on), plus its control socket for keyframe requests and input.
// The phone's encoder is a single resource — a second server aborts — so
// every viewer shares this one and gets the frames fanned out.
type capture struct {
	srv   *server
	video net.Conn
	ctl   net.Conn

	mu     sync.Mutex
	w, h   int
	done   bool
	config []byte // SPS/PPS, re-sent ahead of every keyframe
	subs   map[*Stream]func(data []byte, key bool, ptsMs int64)
}

var (
	capMu sync.Mutex
	cap   *capture // the live capture, if any viewer has one open
)

// liveStream is the live capture, if any.
func liveStream() *capture {
	capMu.Lock()
	defer capMu.Unlock()
	return cap
}

// Stream is one viewer's handle on the shared capture.
type Stream struct{ c *capture }

// StartStream delivers H.264 frames of the display to cb from a reader
// goroutine until Stop. The first viewer starts the capture; later ones join
// it (and get a fresh keyframe). maxDim caps the long side.
func StartStream(maxDim, fps, bitrate int, cb func(data []byte, key bool, ptsMs int64)) (*Stream, error) {
	capMu.Lock()
	defer capMu.Unlock()
	if cap != nil {
		st := &Stream{c: cap}
		cap.mu.Lock()
		cap.subs[st] = cb
		cap.mu.Unlock()
		cap.ForceKeyframe()
		return st, nil
	}
	c, err := startCapture(maxDim, fps, bitrate)
	if err != nil {
		return nil, err
	}
	st := &Stream{c: c}
	c.mu.Lock()
	c.subs[st] = cb
	c.mu.Unlock()
	cap = c
	return st, nil
}

// Size is the picture size (changes on rotation).
func (st *Stream) Size() (w, h int) { return st.c.Size() }

// ForceKeyframe asks for a fresh SPS/PPS + IDR (viewer recovery).
func (st *Stream) ForceKeyframe() { st.c.ForceKeyframe() }

// Stop detaches this viewer; the last one out ends the capture.
func (st *Stream) Stop() {
	c := st.c
	c.mu.Lock()
	delete(c.subs, st)
	n := len(c.subs)
	c.mu.Unlock()
	if n == 0 {
		c.stop()
	}
}

func startCapture(maxDim, fps, bitrate int) (*capture, error) {
	srv, err := startServer("video=true", "audio=false", "control=true", "video_codec=h264",
		fmt.Sprintf("max_size=%d", maxDim), fmt.Sprintf("video_bit_rate=%d", bitrate), fmt.Sprintf("max_fps=%d", fps),
		"send_frame_meta=true", "send_stream_meta=true", "stay_awake=true")
	if err != nil {
		return nil, err
	}
	video, err := srv.dial(true)
	if err != nil {
		srv.stop()
		return nil, err
	}
	ctl, err := srv.dial(false)
	if err != nil {
		video.Close()
		srv.stop()
		return nil, err
	}
	var codec [4]byte
	video.SetReadDeadline(time.Now().Add(8 * time.Second))
	if _, err := io.ReadFull(video, codec[:]); err != nil {
		ctl.Close()
		video.Close()
		srv.stop()
		return nil, fmt.Errorf("scrcpy video: %w", err)
	}
	s := &capture{srv: srv, video: video, ctl: ctl, subs: map[*Stream]func([]byte, bool, int64){}}
	// the first packet is the session header (size); wait for it so the
	// caller knows the geometry, then keep reading in the background
	if err := s.readPacket(); err != nil {
		s.stop()
		return nil, fmt.Errorf("scrcpy video: %w", err)
	}
	video.SetReadDeadline(time.Time{})
	go s.loop()
	go func() { io.Copy(io.Discard, ctl) }() // device messages (clipboard…): not used
	return s, nil
}

// Size is the capture's current picture size (changes on rotation).
func (s *capture) Size() (w, h int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w, s.h
}

const (
	flagSession = uint32(1) << 31 // in the first 4 bytes of a 12-byte header
	flagConfig  = uint64(1) << 62 // in the 8-byte pts field
	flagKey     = uint64(1) << 61
	ptsMask     = flagKey - 1
)

// readPacket reads one 12-byte header and what follows: a session header
// (new size) or a frame (config or media).
func (s *capture) readPacket() error {
	var hdr [12]byte
	if _, err := io.ReadFull(s.video, hdr[:]); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(hdr[0:4])&flagSession != 0 {
		w := int(binary.BigEndian.Uint32(hdr[4:8]))
		h := int(binary.BigEndian.Uint32(hdr[8:12]))
		s.mu.Lock()
		s.w, s.h = w, h
		s.mu.Unlock()
		return nil
	}
	ptsFlags := binary.BigEndian.Uint64(hdr[0:8])
	n := binary.BigEndian.Uint32(hdr[8:12])
	if n > 16<<20 {
		return fmt.Errorf("frame of %d bytes", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(s.video, data); err != nil {
		return err
	}
	if ptsFlags&flagConfig != 0 {
		s.mu.Lock()
		s.config = data
		s.mu.Unlock()
		return nil
	}
	key := ptsFlags&flagKey != 0
	s.mu.Lock()
	if key && len(s.config) > 0 {
		data = append(append([]byte{}, s.config...), data...)
	}
	subs := make([]func([]byte, bool, int64), 0, len(s.subs))
	for _, cb := range s.subs {
		subs = append(subs, cb)
	}
	s.mu.Unlock()
	for _, cb := range subs {
		cb(data, key, int64(ptsFlags&ptsMask)/1000)
	}
	return nil
}

func (s *capture) loop() {
	for {
		if err := s.readPacket(); err != nil {
			s.mu.Lock()
			done := s.done
			s.mu.Unlock()
			if !done && !errors.Is(err, io.EOF) {
				log.Printf("screen capture ended: %v", err)
			}
			s.stop()
			return
		}
	}
}

// ForceKeyframe restarts the encoder, which emits fresh SPS/PPS and an IDR.
func (s *capture) ForceKeyframe() {
	s.ctl.Write([]byte{msgResetVideo})
}

// stop ends the capture; no callbacks after it returns.
func (s *capture) stop() {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	s.mu.Unlock()
	capMu.Lock()
	if cap == s {
		cap = nil
	}
	capMu.Unlock()
	s.video.Close()
	s.ctl.Close()
	s.srv.stop()
}

// --- input: scrcpy control messages. Events ride the live video stream's
// control socket when there is one (coordinates then map through its picture
// size, rotation included); otherwise a control-only server is kept for the
// purpose and coordinates are display pixels.

const (
	msgInjectKeycode = 0
	msgInjectText    = 1
	msgInjectTouch   = 2
	msgInjectScroll  = 3
	msgResetVideo    = 17

	actionDown = 0
	actionUp   = 1
	actionMove = 2

	pointerFinger = -2 // POINTER_ID_GENERIC_FINGER: a touchscreen finger, not a mouse
)

var (
	ctlMu     sync.Mutex
	ctlOnly   *server
	ctlConn   net.Conn
	sizeCache struct {
		w, h int
		at   time.Time
	}
)

// channel picks the control socket for an input event and the pixel size
// its coordinates are expressed in.
func channel() (c net.Conn, w, h int, err error) {
	if s := liveStream(); s != nil {
		w, h := s.Size()
		if w > 0 && h > 0 {
			return s.ctl, w, h, nil
		}
	}
	ctlMu.Lock()
	defer ctlMu.Unlock()
	if ctlConn == nil {
		srv, err := startServer("video=false", "audio=false", "control=true")
		if err != nil {
			return nil, 0, 0, err
		}
		c, err := srv.dial(true)
		if err != nil {
			srv.stop()
			return nil, 0, 0, err
		}
		ctlOnly, ctlConn = srv, c
		go func() { // device messages: not used; EOF means the server died
			io.Copy(io.Discard, c)
			ctlMu.Lock()
			if ctlConn == c {
				ctlConn = nil
				ctlOnly.stop()
				ctlOnly = nil
			}
			ctlMu.Unlock()
		}()
	}
	if time.Since(sizeCache.at) > 5*time.Second {
		sizeCache.w, sizeCache.h = wmSize()
		sizeCache.at = time.Now()
	}
	if sizeCache.w == 0 {
		return nil, 0, 0, errors.New("display size unknown")
	}
	return ctlConn, sizeCache.w, sizeCache.h, nil
}

func send(b []byte) error {
	c, _, _, err := channel()
	if err != nil {
		return err
	}
	_, err = c.Write(b)
	return err
}

func putPos(b []byte, nx, ny float64, w, h int) []byte {
	x := int32(math.Round(clamp01(nx) * float64(w-1)))
	y := int32(math.Round(clamp01(ny) * float64(h-1)))
	b = binary.BigEndian.AppendUint32(b, uint32(x))
	b = binary.BigEndian.AppendUint32(b, uint32(y))
	b = binary.BigEndian.AppendUint16(b, uint16(w))
	b = binary.BigEndian.AppendUint16(b, uint16(h))
	return b
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// Touch injects one finger event at normalized (nx, ny): actionDown,
// actionUp or actionMove.
func Touch(nx, ny float64, action byte) error {
	c, w, h, err := channel()
	if err != nil {
		return err
	}
	b := []byte{msgInjectTouch, action}
	var finger int64 = pointerFinger
	b = binary.BigEndian.AppendUint64(b, uint64(finger))
	b = putPos(b, nx, ny, w, h)
	pressure := uint16(0xffff)
	buttons := uint32(1) // PRIMARY
	if action == actionUp {
		pressure, buttons = 0, 0
	}
	b = binary.BigEndian.AppendUint16(b, pressure)
	b = binary.BigEndian.AppendUint32(b, 1) // action button: PRIMARY
	b = binary.BigEndian.AppendUint32(b, buttons)
	_, err = c.Write(b)
	return err
}

// TouchDown/Up/Move are Touch with the action spelled out.
func TouchDown(nx, ny float64) error { return Touch(nx, ny, actionDown) }
func TouchUp(nx, ny float64) error   { return Touch(nx, ny, actionUp) }
func TouchMove(nx, ny float64) error { return Touch(nx, ny, actionMove) }

// Scroll injects a wheel event at (nx, ny): hs/vs in notches, positive =
// content moves down/right the way Android counts (a wheel rolled away from
// the user is positive vertical).
func Scroll(nx, ny, hs, vs float64) error {
	c, w, h, err := channel()
	if err != nil {
		return err
	}
	b := []byte{msgInjectScroll}
	b = putPos(b, nx, ny, w, h)
	b = binary.BigEndian.AppendUint16(b, uint16(i16fp(hs/16)))
	b = binary.BigEndian.AppendUint16(b, uint16(i16fp(vs/16)))
	b = binary.BigEndian.AppendUint32(b, 0)
	_, err = c.Write(b)
	return err
}

// i16fp is scrcpy's signed 16-bit fixed point for [-1, 1].
func i16fp(f float64) int16 {
	f = math.Max(-1, math.Min(1, f))
	if f >= 1 {
		return 0x7fff
	}
	return int16(math.Round(f * 0x8000))
}

// Key presses or releases an Android keycode with the given meta state.
func Key(keycode int, down bool, meta int) error {
	action := byte(actionUp)
	if down {
		action = actionDown
	}
	b := []byte{msgInjectKeycode, action}
	b = binary.BigEndian.AppendUint32(b, uint32(keycode))
	b = binary.BigEndian.AppendUint32(b, 0)
	b = binary.BigEndian.AppendUint32(b, uint32(meta))
	return send(b)
}

// Text types a string into the focused field (unicode, layout-independent).
func Text(s string) error {
	for len(s) > 0 { // the server caps one message at 300 bytes
		n := len(s)
		if n > 300 {
			n = 300
			for n > 0 && s[n]&0xC0 == 0x80 { // don't split a rune
				n--
			}
		}
		b := []byte{msgInjectText}
		b = binary.BigEndian.AppendUint32(b, uint32(n))
		b = append(b, s[:n]...)
		if err := send(b); err != nil {
			return err
		}
		s = s[n:]
	}
	return nil
}

// ReleaseInput ends the control-only server (a viewer's session closed and
// nothing else needs it). Live video streams keep their own.
func ReleaseInput() {
	ctlMu.Lock()
	defer ctlMu.Unlock()
	if ctlConn != nil {
		ctlConn.Close()
		ctlConn = nil
	}
	if ctlOnly != nil {
		ctlOnly.stop()
		ctlOnly = nil
	}
}
