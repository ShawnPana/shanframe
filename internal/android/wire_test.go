package android

import (
	"encoding/binary"
	"testing"
)

func TestPutPos(t *testing.T) {
	b := putPos(nil, 0.5, 1, 720, 1600)
	if len(b) != 12 {
		t.Fatalf("len %d", len(b))
	}
	if x := binary.BigEndian.Uint32(b[0:4]); x != 360 { // round(0.5*719)
		t.Errorf("x %d", x)
	}
	if y := binary.BigEndian.Uint32(b[4:8]); y != 1599 {
		t.Errorf("y %d", y)
	}
	if w := binary.BigEndian.Uint16(b[8:10]); w != 720 {
		t.Errorf("w %d", w)
	}
	b = putPos(nil, -1, 2, 10, 10) // clamped
	if x := binary.BigEndian.Uint32(b[0:4]); x != 0 {
		t.Errorf("clamp x %d", x)
	}
}

func TestI16FP(t *testing.T) {
	for _, c := range []struct {
		f float64
		v int16
	}{{0, 0}, {1, 0x7fff}, {2, 0x7fff}, {-1, -0x8000}, {0.5, 0x4000}, {-0.5, -0x4000}} {
		if got := i16fp(c.f); got != c.v {
			t.Errorf("i16fp(%v) = %#x want %#x", c.f, got, c.v)
		}
	}
}

func TestHeaderFlags(t *testing.T) {
	// a session header: top bit of the first 4 bytes set, then width/height
	var hdr [12]byte
	binary.BigEndian.PutUint32(hdr[0:4], flagSession)
	binary.BigEndian.PutUint32(hdr[4:8], 720)
	binary.BigEndian.PutUint32(hdr[8:12], 1600)
	if binary.BigEndian.Uint32(hdr[0:4])&flagSession == 0 {
		t.Fatal("session flag not detected")
	}
	// a keyframe header: pts with the key flag
	pts := uint64(123456) | flagKey
	binary.BigEndian.PutUint64(hdr[0:8], pts)
	if binary.BigEndian.Uint32(hdr[0:4])&flagSession != 0 {
		t.Fatal("keyframe misread as session header")
	}
	if pts&flagConfig != 0 || pts&flagKey == 0 || pts&ptsMask != 123456 {
		t.Fatal("pts flags")
	}
}

func TestParseBrokerArgs(t *testing.T) {
	if _, _, err := ParseBrokerArgs([]string{"x"}); err == nil {
		t.Error("want error")
	}
	p, tok, err := ParseBrokerArgs([]string{"41234", "abc"})
	if err != nil || p != 41234 || tok != "abc" {
		t.Errorf("got %d %q %v", p, tok, err)
	}
}
