//go:build darwin && cgo

// Package input injects mouse and keyboard events into the local session —
// the receiving end of a native screen session. macOS: CGEvents (the same
// mechanism assistive software uses). Needs Accessibility permission.
package input

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreGraphics -framework Foundation
#include <ApplicationServices/ApplicationServices.h>

static int sfAXTrusted(int prompt) {
	if (!prompt) return AXIsProcessTrusted() ? 1 : 0;
	const void *keys[] = { kAXTrustedCheckOptionPrompt };
	const void *vals[] = { kCFBooleanTrue };
	CFDictionaryRef opts = CFDictionaryCreate(NULL, keys, vals, 1,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	Boolean ok = AXIsProcessTrustedWithOptions(opts);
	CFRelease(opts);
	return ok ? 1 : 0;
}

static void sfDisplaySize(double *w, double *h) {
	CGRect b = CGDisplayBounds(CGMainDisplayID());
	*w = b.size.width; *h = b.size.height;
}

static void sfMouse(int type, int btn, double x, double y, int clicks, uint64_t flags) {
	CGEventRef e = CGEventCreateMouseEvent(NULL, (CGEventType)type,
		CGPointMake(x, y), (CGMouseButton)btn);
	if (clicks > 1) CGEventSetIntegerValueField(e, kCGMouseEventClickState, clicks);
	CGEventSetFlags(e, (CGEventFlags)flags);
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

static void sfWheel(double dx, double dy, uint64_t flags) {
	CGEventRef e = CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitPixel, 2,
		(int32_t)(-dy), (int32_t)(-dx));
	CGEventSetFlags(e, (CGEventFlags)flags);
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

static void sfKey(int code, int down, uint64_t flags) {
	CGEventRef e = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)code, down != 0);
	CGEventSetFlags(e, (CGEventFlags)flags);
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

static void sfText(const uint16_t *chars, int n, uint64_t flags) {
	CGEventRef d = CGEventCreateKeyboardEvent(NULL, 0, true);
	CGEventKeyboardSetUnicodeString(d, n, chars);
	CGEventSetFlags(d, (CGEventFlags)flags);
	CGEventPost(kCGHIDEventTap, d);
	CFRelease(d);
	CGEventRef u = CGEventCreateKeyboardEvent(NULL, 0, false);
	CGEventKeyboardSetUnicodeString(u, n, chars);
	CGEventPost(kCGHIDEventTap, u);
	CFRelease(u);
}
*/
import "C"

import (
	"math"
	"sync"
	"time"
	"unicode/utf16"
)

// Injector is one remote controller's input state (modifiers, drag, clicks).
type Injector struct {
	mu                   sync.Mutex
	mods                 map[string]bool // "cmd","ctrl","alt","shift"
	buttons              int             // bitmask of pressed buttons: 1 left, 2 right, 4 middle
	x, y                 float64         // current pointer, display points
	lastDown             time.Time
	lastBtn              int
	lastDownX, lastDownY float64 // pointer at the previous button-down
	clicks               int
}

func Supported() bool  { return true }
func Authorized() bool { return C.sfAXTrusted(0) != 0 }

// RequestPermission shows the macOS Accessibility prompt (once per app).
func RequestPermission() { C.sfAXTrusted(1) }

// DisplaySize is the main display in points — the coordinate space of
// screenshots and of the positions input events are given in.
func DisplaySize() (w, h float64) {
	var cw, ch C.double
	C.sfDisplaySize(&cw, &ch)
	return float64(cw), float64(ch)
}

func New() *Injector {
	var w, h C.double
	C.sfDisplaySize(&w, &h)
	return &Injector{mods: map[string]bool{}, x: float64(w) / 2, y: float64(h) / 2}
}

func (in *Injector) flags() C.uint64_t {
	var f uint64
	if in.mods["cmd"] {
		f |= 1 << 20 // kCGEventFlagMaskCommand
	}
	if in.mods["shift"] {
		f |= 1 << 17
	}
	if in.mods["ctrl"] {
		f |= 1 << 18
	}
	if in.mods["alt"] {
		f |= 1 << 19
	}
	return C.uint64_t(f)
}

// Move places the pointer at (nx, ny), normalized 0..1 over the main display.
func (in *Injector) Move(nx, ny float64) {
	in.mu.Lock()
	defer in.mu.Unlock()
	var w, h C.double
	C.sfDisplaySize(&w, &h)
	in.x = clamp01(nx) * float64(w)
	in.y = clamp01(ny) * float64(h)
	typ, btn := C.int(5), C.int(0) // kCGEventMouseMoved
	switch {
	case in.buttons&1 != 0:
		typ, btn = 6, 0 // kCGEventLeftMouseDragged
	case in.buttons&2 != 0:
		typ, btn = 7, 1 // kCGEventRightMouseDragged
	case in.buttons&4 != 0:
		typ, btn = 27, 2 // kCGEventOtherMouseDragged
	}
	C.sfMouse(typ, btn, C.double(in.x), C.double(in.y), 1, in.flags())
}

// Button presses or releases button b (0 left, 1 right, 2 middle) at the
// current pointer. Consecutive fast clicks in the same place become double/
// triple clicks — macOS resets the count when the mouse moves between clicks,
// and so do we, or tap-move-tap on a phone reads as a double-click and
// selects a word.
func (in *Injector) Button(b int, down bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	mask := 1 << b
	if down {
		samePlace := math.Hypot(in.x-in.lastDownX, in.y-in.lastDownY) <= 8
		if b == in.lastBtn && samePlace && time.Since(in.lastDown) < 400*time.Millisecond {
			in.clicks++
		} else {
			in.clicks = 1
		}
		in.lastDown, in.lastBtn = time.Now(), b
		in.lastDownX, in.lastDownY = in.x, in.y
		in.buttons |= mask
	} else {
		in.buttons &^= mask
	}
	var typ C.int
	switch b {
	case 0:
		typ = map[bool]C.int{true: 1, false: 2}[down] // left down/up
	case 1:
		typ = map[bool]C.int{true: 3, false: 4}[down] // right down/up
	default:
		typ = map[bool]C.int{true: 25, false: 26}[down] // other down/up
	}
	C.sfMouse(typ, C.int(b), C.double(in.x), C.double(in.y), C.int(in.clicks), in.flags())
}

// Wheel scrolls by (dx, dy) pixels at the current pointer.
func (in *Injector) Wheel(dx, dy float64) {
	in.mu.Lock()
	defer in.mu.Unlock()
	C.sfWheel(C.double(dx), C.double(dy), in.flags())
}

// keycodes for named (non-text) keys — ANSI virtual keycodes.
var keycodes = map[string]int{
	"Enter": 36, "Tab": 48, "Space": 49, "Backspace": 51, "Escape": 53,
	"Delete": 117, "Home": 115, "End": 119, "PageUp": 116, "PageDown": 121,
	"ArrowLeft": 123, "ArrowRight": 124, "ArrowDown": 125, "ArrowUp": 126,
	"F1": 122, "F2": 120, "F3": 99, "F4": 118, "F5": 96, "F6": 97,
	"F7": 98, "F8": 100, "F9": 101, "F10": 109, "F11": 103, "F12": 111,
	"Meta": 55, "Shift": 56, "CapsLock": 57, "Alt": 58, "Control": 59,
}

var modOf = map[string]string{"Meta": "cmd", "Shift": "shift", "Alt": "alt", "Control": "ctrl"}

// Key presses or releases a named key ("Enter", "ArrowUp", "Meta", …).
// Modifiers stick until released and flag every other event.
func (in *Injector) Key(name string, down bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if m, ok := modOf[name]; ok {
		in.mods[m] = down
	}
	code, ok := keycodes[name]
	if !ok {
		if r := []rune(name); len(r) == 1 && down {
			in.mu.Unlock()
			in.Text(name)
			in.mu.Lock()
		}
		return
	}
	C.sfKey(C.int(code), C.int(b2i(down)), in.flags())
}

// Text types a string (unicode, layout-independent). Modifier-carrying
// single characters go through Key/flags instead when a modifier is held.
func (in *Injector) Text(s string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.mods["cmd"] || in.mods["ctrl"] || in.mods["alt"] {
		// a shortcut like cmd+c must be a real key event, not typed text
		for _, r := range s {
			if code, ok := ansiKeycode[r]; ok {
				C.sfKey(C.int(code), 1, in.flags())
				C.sfKey(C.int(code), 0, in.flags())
			}
		}
		return
	}
	u := utf16.Encode([]rune(s))
	for len(u) > 0 {
		n := len(u)
		if n > 20 {
			n = 20
		}
		C.sfText((*C.uint16_t)(&u[0]), C.int(n), in.flags())
		u = u[n:]
	}
}

// ReleaseAll lifts everything held — called when a session ends so a
// disconnect can't leave a button or modifier stuck down.
func (in *Injector) ReleaseAll() {
	in.mu.Lock()
	defer in.mu.Unlock()
	for b, typ := range map[int]C.int{0: 2, 1: 4, 2: 26} {
		if in.buttons&(1<<b) != 0 {
			C.sfMouse(typ, C.int(b), C.double(in.x), C.double(in.y), 1, 0)
		}
	}
	in.buttons = 0
	for name, m := range modOf {
		if in.mods[m] {
			C.sfKey(C.int(keycodes[name]), 0, 0)
			in.mods[m] = false
		}
	}
}

// ansiKeycode maps characters to US-ANSI virtual keycodes, for shortcuts.
var ansiKeycode = map[rune]int{
	'a': 0, 'b': 11, 'c': 8, 'd': 2, 'e': 14, 'f': 3, 'g': 5, 'h': 4, 'i': 34,
	'j': 38, 'k': 40, 'l': 37, 'm': 46, 'n': 45, 'o': 31, 'p': 35, 'q': 12,
	'r': 15, 's': 1, 't': 17, 'u': 32, 'v': 9, 'w': 13, 'x': 7, 'y': 16, 'z': 6,
	'0': 29, '1': 18, '2': 19, '3': 20, '4': 21, '5': 23, '6': 22, '7': 26,
	'8': 28, '9': 25, '-': 27, '=': 24, '[': 33, ']': 30, '\\': 42, ';': 41,
	'\'': 39, ',': 43, '.': 47, '/': 44, '`': 50, ' ': 49,
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
