package main

// Screen verbs (controller side): screenshot, click/tap/drag/scroll/type/key,
// in the device's point coordinates — the same space the screenshot is in —
// so an agent can look, decide, act. One session per verb, or `batch` for
// many actions over one connection.

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shawnpana/shanframe/internal/frame"
	"github.com/shawnpana/shanframe/internal/peer"
	"github.com/shawnpana/shanframe/internal/rendezvous"
)

type screenSession struct {
	c      *client
	dev    *rendezvous.Device
	conn   *peer.Conn
	s      io.ReadWriteCloser // "screen" stream: input events out, cursor/notes in
	w, h   float64            // display size in points
	done   func()
	noteIn string
}

func openScreen(target string) (*screenSession, error) {
	c, cancel, err := newClient()
	if err != nil {
		return nil, err
	}
	dev, err := c.target(target)
	if err != nil {
		cancel()
		return nil, err
	}
	s, conn, err := c.openConn(dev, rendezvous.Open{Service: "screen"})
	if err != nil {
		cancel()
		return nil, err
	}
	ss := &screenSession{c: c, dev: dev, conn: conn, s: s, done: func() { s.Close(); conn.Close(); cancel() }}
	// wait for {"t":"ready","w":…,"h":…}; everything after (cursor shapes) is ignored
	deadline := time.After(10 * time.Second)
	ready := make(chan error, 1)
	go func() {
		for {
			typ, p, err := frame.Read(s)
			if err != nil {
				ready <- err
				return
			}
			if typ == frame.Error {
				ready <- errors.New(string(p))
				return
			}
			var m struct {
				T    string  `json:"t"`
				W    float64 `json:"w"`
				H    float64 `json:"h"`
				Note string  `json:"note"`
			}
			if json.Unmarshal(p, &m) != nil {
				continue
			}
			switch m.T {
			case "noinput":
				ss.noteIn = m.Note
			case "ready":
				ss.w, ss.h = m.W, m.H
				ready <- nil
				go io.Copy(io.Discard, s) // drain cursor updates
				return
			}
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			ss.done()
			return nil, err
		}
	case <-deadline:
		ss.done()
		return nil, errors.New("screen didn't answer")
	}
	if ss.noteIn != "" {
		fmt.Fprintln(os.Stderr, "shanframe:", ss.noteIn)
	}
	if ss.w == 0 || ss.h == 0 {
		ss.done()
		return nil, errors.New("this device doesn't report a display size (screen verbs need the native screen)")
	}
	return ss, nil
}

func (ss *screenSession) send(ev any) {
	b, _ := json.Marshal(ev)
	frame.Write(ss.s, frame.Data, b)
}

func (ss *screenSession) move(x, y float64) {
	ss.send(map[string]any{"t": "mv", "x": x / ss.w, "y": y / ss.h})
}

func (ss *screenSession) button(b int, down bool) {
	ss.send(map[string]any{"t": "btn", "b": b, "d": down})
}

func (ss *screenSession) click(x, y float64, btn, count int) {
	ss.move(x, y)
	time.Sleep(30 * time.Millisecond)
	for i := 0; i < count; i++ {
		ss.button(btn, true)
		time.Sleep(15 * time.Millisecond)
		ss.button(btn, false)
		if i+1 < count {
			time.Sleep(60 * time.Millisecond)
		}
	}
}

func (ss *screenSession) drag(x1, y1, x2, y2 float64) {
	ss.move(x1, y1)
	time.Sleep(30 * time.Millisecond)
	ss.button(0, true)
	time.Sleep(80 * time.Millisecond)
	const steps = 12
	for i := 1; i <= steps; i++ {
		t := float64(i) / steps
		ss.move(x1+(x2-x1)*t, y1+(y2-y1)*t)
		time.Sleep(16 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond)
	ss.button(0, false)
}

func (ss *screenSession) scroll(x, y, dx, dy float64) {
	ss.move(x, y)
	time.Sleep(20 * time.Millisecond)
	ss.send(map[string]any{"t": "wheel", "dx": dx, "dy": dy})
}

func (ss *screenSession) typeText(t string) { ss.send(map[string]any{"t": "txt", "s": t}) }

// keyNames maps CLI spellings to the DOM key names the agent understands.
var keyNames = map[string]string{
	"cmd": "Meta", "command": "Meta", "meta": "Meta", "super": "Meta", "win": "Meta",
	"ctrl": "Control", "control": "Control", "alt": "Alt", "opt": "Alt", "option": "Alt", "shift": "Shift",
	"enter": "Enter", "return": "Enter", "esc": "Escape", "escape": "Escape", "tab": "Tab", "space": "Space",
	"backspace": "Backspace", "delete": "Delete", "del": "Delete", "up": "ArrowUp", "down": "ArrowDown",
	"left": "ArrowLeft", "right": "ArrowRight", "home": "Home", "end": "End", "pageup": "PageUp", "pagedown": "PageDown",
	"f1": "F1", "f2": "F2", "f3": "F3", "f4": "F4", "f5": "F5", "f6": "F6", "f7": "F7", "f8": "F8", "f9": "F9", "f10": "F10", "f11": "F11", "f12": "F12",
}

// key presses a combo like "cmd+shift+t", "enter", "ctrl+c", "a".
func (ss *screenSession) key(combo string) error {
	parts := strings.Split(combo, "+")
	if combo == "+" || strings.HasSuffix(combo, "++") {
		parts = append(parts[:len(parts)-2], "+") // literal plus
	}
	var names []string
	for _, p := range parts {
		n := p
		if v, ok := keyNames[strings.ToLower(p)]; ok {
			n = v
		}
		if n == "" {
			return fmt.Errorf("bad key combo %q", combo)
		}
		names = append(names, n)
	}
	for _, n := range names {
		ss.send(map[string]any{"t": "key", "k": n, "d": true})
		time.Sleep(10 * time.Millisecond)
	}
	for i := len(names) - 1; i >= 0; i-- {
		ss.send(map[string]any{"t": "key", "k": names[i], "d": false})
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// screenshot saves one PNG over a second stream on the same session.
func (ss *screenSession) screenshot(path string) (w, h int, err error) {
	s, err := ss.conn.Dial(rendezvous.Open{Service: "screenshot"}, 15*time.Second)
	if err != nil {
		return 0, 0, err
	}
	defer s.Close()
	return readScreenshot(s, path)
}

func readScreenshot(s io.Reader, path string) (w, h int, err error) {
	var hdr struct{ W, H, Bytes int }
	var buf []byte
	for {
		typ, p, err := frame.Read(s)
		if err != nil {
			return 0, 0, fmt.Errorf("screenshot: %v", err)
		}
		switch typ {
		case frame.Error:
			return 0, 0, errors.New(string(p))
		case frame.Data:
			if hdr.W == 0 {
				if json.Unmarshal(p, &hdr) != nil || hdr.W == 0 {
					return 0, 0, errors.New("screenshot: bad header")
				}
				buf = make([]byte, 0, hdr.Bytes)
				continue
			}
			buf = append(buf, p...)
		case frame.Exit:
			if len(buf) == 0 {
				return 0, 0, errors.New("screenshot: empty")
			}
			if path == "-" {
				_, err = os.Stdout.Write(buf)
			} else {
				err = os.WriteFile(path, buf, 0o644)
			}
			return hdr.W, hdr.H, err
		}
	}
}

// screenshotOnly is the cheap path: one stream, no input session.
func screenshotOnly(target, path string, asJSON bool) error {
	c, cancel, err := newClient()
	if err != nil {
		return err
	}
	defer cancel()
	dev, err := c.target(target)
	if err != nil {
		return err
	}
	s, done, err := c.open(dev, rendezvous.Open{Service: "screenshot"})
	if err != nil {
		return err
	}
	defer done()
	w, h, err := readScreenshot(s, path)
	if err != nil {
		return err
	}
	report(asJSON, map[string]any{"file": path, "w": w, "h": h}, fmt.Sprintf("%s  %dx%d", path, w, h))
	return nil
}

func report(asJSON bool, obj map[string]any, text string) {
	if asJSON {
		json.NewEncoder(os.Stdout).Encode(obj)
	} else if text != "" {
		fmt.Println(text)
	}
}

// screenVerb runs one screen action; `batch` reads many from stdin.
func screenVerb(target string, verb string, args []string) error {
	asJSON := false
	var rest []string
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		} else {
			rest = append(rest, a)
		}
	}
	if verb == "screenshot" {
		path := "screenshot.png"
		if len(rest) > 0 {
			path = rest[0]
		}
		return screenshotOnly(target, path, asJSON)
	}
	ss, err := openScreen(target)
	if err != nil {
		return err
	}
	defer ss.done()
	if verb == "size" {
		report(asJSON, map[string]any{"w": ss.w, "h": ss.h}, fmt.Sprintf("%.0fx%.0f", ss.w, ss.h))
		return nil
	}
	if verb == "batch" {
		sc := bufio.NewScanner(os.Stdin)
		n := 0
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			f := strings.Fields(line)
			if err := ss.do(f[0], f[1:], asJSON); err != nil {
				return fmt.Errorf("line %d (%s): %v", n+1, line, err)
			}
			n++
		}
		time.Sleep(150 * time.Millisecond)
		return nil
	}
	if err := ss.do(verb, rest, asJSON); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond) // let the last events leave before the channel closes
	return nil
}

func (ss *screenSession) do(verb string, a []string, asJSON bool) error {
	num := func(i int) (float64, error) {
		if i >= len(a) {
			return 0, fmt.Errorf("%s: not enough arguments", verb)
		}
		return strconv.ParseFloat(a[i], 64)
	}
	switch verb {
	case "click", "tap", "rightclick", "doubleclick", "dblclick", "middleclick":
		x, err := num(0)
		if err != nil {
			return err
		}
		y, err := num(1)
		if err != nil {
			return err
		}
		btn, count := 0, 1
		switch verb {
		case "rightclick":
			btn = 1
		case "middleclick":
			btn = 2
		case "doubleclick", "dblclick":
			count = 2
		}
		for _, f := range a[2:] {
			switch f {
			case "--right":
				btn = 1
			case "--middle":
				btn = 2
			case "--double":
				count = 2
			}
		}
		ss.click(x, y, btn, count)
	case "drag", "swipe":
		var v [4]float64
		for i := range v {
			f, err := num(i)
			if err != nil {
				return err
			}
			v[i] = f
		}
		ss.drag(v[0], v[1], v[2], v[3])
	case "scroll": // scroll x y dy  |  scroll x y dx dy
		x, err := num(0)
		if err != nil {
			return err
		}
		y, err := num(1)
		if err != nil {
			return err
		}
		dx, dy := 0.0, 0.0
		if len(a) >= 4 {
			dx, _ = num(2)
			dy, _ = num(3)
		} else {
			dy, err = num(2)
			if err != nil {
				return err
			}
		}
		ss.scroll(x, y, dx, dy)
	case "move":
		x, err := num(0)
		if err != nil {
			return err
		}
		y, err := num(1)
		if err != nil {
			return err
		}
		ss.move(x, y)
	case "type":
		if len(a) == 0 {
			return errors.New("type: nothing to type")
		}
		ss.typeText(strings.Join(a, " "))
	case "key":
		if len(a) == 0 {
			return errors.New("key: which key?")
		}
		for _, k := range a {
			if err := ss.key(k); err != nil {
				return err
			}
			time.Sleep(40 * time.Millisecond)
		}
	case "sleep":
		d, err := num(0)
		if err != nil {
			return err
		}
		time.Sleep(time.Duration(d * float64(time.Second)))
	case "screenshot":
		path := "screenshot.png"
		if len(a) > 0 {
			path = a[0]
		}
		time.Sleep(120 * time.Millisecond) // let the UI settle after the previous action
		w, h, err := ss.screenshot(path)
		if err != nil {
			return err
		}
		report(asJSON, map[string]any{"file": path, "w": w, "h": h}, fmt.Sprintf("%s  %dx%d", path, w, h))
	default:
		return fmt.Errorf("unknown screen verb %q", verb)
	}
	return nil
}

var _ = binary.BigEndian
