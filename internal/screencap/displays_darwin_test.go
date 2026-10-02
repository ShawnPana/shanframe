//go:build darwin && cgo

package screencap

import "testing"

// Displays needs no permission (CoreGraphics + AppKit), so this runs anywhere
// with a screen: the main display is number 1, sizes are sane, and Pick
// answers out-of-range numbers in plain words.
func TestDisplays(t *testing.T) {
	ds := Displays()
	if len(ds) == 0 {
		t.Skip("no displays (headless)")
	}
	t.Logf("displays: %+v", ds)
	if !ds[0].Main || ds[0].N != 1 {
		t.Fatalf("first display must be the main one, numbered 1: %+v", ds[0])
	}
	for i, d := range ds {
		if d.N != i+1 || d.W <= 0 || d.H <= 0 || d.Name == "" {
			t.Fatalf("display %d malformed: %+v", i, d)
		}
	}
	if d, err := Pick(ds, 0); err != nil || d.N != 1 {
		t.Fatalf("Pick(0) should be the main display: %+v %v", d, err)
	}
	if _, err := Pick(ds, len(ds)+1); err == nil {
		t.Fatalf("Pick past the end must fail")
	}
	if _, _, _, err := Still(len(ds) + 1); err == nil {
		t.Fatalf("Still past the end must fail before touching ScreenCaptureKit")
	}
}
