//go:build !darwin || !cgo

package input

// Without native injection of its own, a platform has input only when it is
// an Android phone: events become touches, keys and text through the
// phone's own debugging channel (internal/android).

import (
	"strings"
	"unicode/utf8"

	"github.com/shawnpana/shanframe/internal/android"
)

// Injector is one remote controller's input state: pointer position, the
// finger (left button) down or not, and sticky modifiers.
type Injector struct {
	x, y  float64
	down  bool
	shift bool
	ctrl  bool
	alt   bool
	meta  bool
}

func Supported() bool  { return android.Available() }
func Authorized() bool { return android.Ready() }

// Note explains view-only mode in the viewer, in plain words.
func Note() string {
	if android.Available() {
		return "view only — screen sharing isn't fully set up on this phone"
	}
	return "view only — this device can't take input"
}

func RequestPermission() {}

func New() *Injector { return &Injector{} }

// DisplaySize is the screen's logical pixel size in its current orientation.
func DisplaySize() (w, h float64) {
	if !android.Available() {
		return 0, 0
	}
	pw, ph := android.DisplaySize()
	return float64(pw), float64(ph)
}

// Move places the pointer at (nx, ny), normalized 0..1. A finger that is
// down drags; a pointer that isn't only remembers where it is.
func (in *Injector) Move(nx, ny float64) {
	in.x, in.y = nx, ny
	if in.down {
		android.TouchMove(nx, ny)
	}
}

// Button maps the controller's buttons onto a phone: left is the finger,
// right is Back, middle is Home (what scrcpy users expect).
func (in *Injector) Button(b int, down bool) {
	switch b {
	case 0:
		if down == in.down {
			return
		}
		in.down = down
		if down {
			android.TouchDown(in.x, in.y)
		} else {
			android.TouchUp(in.x, in.y)
		}
	case 1:
		android.Key(keyBack, down, 0)
	case 2:
		android.Key(keyHome, down, 0)
	}
}

// Wheel scrolls by (dx, dy) pixels at the pointer. Browsers count positive
// dy as "content up" (wheel toward the user); Android counts that negative.
func (in *Injector) Wheel(dx, dy float64) {
	android.Scroll(in.x, in.y, -dx/40, -dy/40)
}

// Android KeyEvent codes and meta flags used here.
const (
	keyHome      = 3
	keyBack      = 4
	keyDpadUp    = 19
	keyDpadDown  = 20
	keyDpadLeft  = 21
	keyDpadRight = 22
	keyVolUp     = 24
	keyVolDown   = 25
	keyPower     = 26
	keyA         = 29
	key0         = 7
	keyAlt       = 57
	keyShift     = 59
	keyTab       = 61
	keySpace     = 62
	keyEnter     = 66
	keyDel       = 67 // backspace
	keyPlayPause = 85
	keyPageUp    = 92
	keyPageDown  = 93
	keyEscape    = 111
	keyFwdDel    = 112
	keyCtrl      = 113
	keyCapsLock  = 115
	keyMeta      = 117
	keyMoveHome  = 122
	keyMoveEnd   = 123
	keyInsert    = 124
	keyF1        = 131
	keyAppSwitch = 187
	keyMenu      = 82
	keySleep     = 223
	keyWakeup    = 224

	metaShift = 1
	metaAlt   = 2
	metaCtrl  = 0x1000
	metaMeta  = 0x10000
)

var named = map[string]int{
	"Enter": keyEnter, "Backspace": keyDel, "Delete": keyFwdDel, "Tab": keyTab,
	"Escape":  keyBack, // a phone has no Esc; Back is what you mean
	"ArrowUp": keyDpadUp, "ArrowDown": keyDpadDown, "ArrowLeft": keyDpadLeft, "ArrowRight": keyDpadRight,
	"Home": keyMoveHome, "End": keyMoveEnd, "PageUp": keyPageUp, "PageDown": keyPageDown, "Insert": keyInsert,
	"Shift": keyShift, "Control": keyCtrl, "Alt": keyAlt, "Meta": keyMeta, "CapsLock": keyCapsLock,
	"ContextMenu": keyAppSwitch, "AudioVolumeUp": keyVolUp, "AudioVolumeDown": keyVolDown, "Power": keyPower,
	"MediaPlayPause": keyPlayPause, " ": keySpace,
	// phone keys by plain name (the CLI passes unknown names through as typed)
	"power": keyPower, "wakeup": keyWakeup, "sleep": keySleep, "back": keyBack, "recents": keyAppSwitch,
	"appswitch": keyAppSwitch, "menu": keyMenu, "volumeup": keyVolUp, "volumedown": keyVolDown, "androidhome": keyHome,
}

func lookupNamed(name string) (int, bool) {
	if c, ok := named[name]; ok {
		return c, true
	}
	c, ok := named[strings.ToLower(name)]
	return c, ok
}

func (in *Injector) metaState() int {
	m := 0
	if in.shift {
		m |= metaShift
	}
	if in.ctrl {
		m |= metaCtrl
	}
	if in.alt {
		m |= metaAlt
	}
	if in.meta {
		m |= metaMeta
	}
	return m
}

// Key presses or releases a named key ("Enter", "ArrowUp", "a", …).
// Printable characters type as text unless a shortcut modifier is held, in
// which case they become keycodes with the modifier state (Ctrl+A, …).
func (in *Injector) Key(name string, down bool) {
	switch name {
	case "Shift":
		in.shift = down
	case "Control":
		in.ctrl = down
	case "Alt":
		in.alt = down
	case "Meta":
		in.meta = down
	}
	if code, ok := lookupNamed(name); ok {
		if name == " " && !in.ctrl && !in.meta && !in.alt {
			if down {
				android.Text(" ")
			}
			return
		}
		android.Key(code, down, in.metaState())
		return
	}
	if utf8.RuneCountInString(name) != 1 {
		if f := strings.TrimPrefix(name, "F"); f != name && len(f) <= 2 { // F1..F12
			n := 0
			for _, c := range f {
				n = n*10 + int(c-'0')
			}
			if n >= 1 && n <= 12 {
				android.Key(keyF1+n-1, down, in.metaState())
			}
		}
		return
	}
	r, _ := utf8.DecodeRuneInString(name)
	if in.ctrl || in.meta || in.alt {
		if code, ok := charKey(r); ok {
			android.Key(code, down, in.metaState())
		}
		return
	}
	if down {
		android.Text(name)
	}
}

// charKey is the keycode for a character that has one (letters, digits).
func charKey(r rune) (int, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return keyA + int(r-'a'), true
	case r >= 'A' && r <= 'Z':
		return keyA + int(r-'A'), true
	case r >= '0' && r <= '9':
		return key0 + int(r-'0'), true
	}
	return 0, false
}

// Text types a string into the focused field.
func (in *Injector) Text(s string) { android.Text(s) }

// ReleaseAll lifts a finger and clears modifiers (controller went away).
func (in *Injector) ReleaseAll() {
	if in.down {
		in.down = false
		android.TouchUp(in.x, in.y)
	}
	for _, k := range []struct {
		on   *bool
		code int
	}{{&in.shift, keyShift}, {&in.ctrl, keyCtrl}, {&in.alt, keyAlt}, {&in.meta, keyMeta}} {
		if *k.on {
			*k.on = false
			android.Key(k.code, false, 0)
		}
	}
}
