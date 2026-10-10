package screencap

import "testing"

// At finds the display under a global point: the main display at the origin,
// a second one to its right, nothing in the gap or beyond.
func TestAtFindsTheDisplayUnderAPoint(t *testing.T) {
	ds := []Display{{N: 1, W: 1512, H: 982, X: 0, Y: 0, Main: true}, {N: 2, W: 1920, H: 1080, X: 1512, Y: -98}}
	for _, c := range []struct {
		x, y float64
		want int
	}{{10, 10, 1}, {1511, 981, 1}, {1512, 0, 2}, {3000, 900, 2}, {-1, 0, 0}, {1600, 985, 0}, {3500, 0, 0}} {
		d, ok := At(ds, c.x, c.y)
		if (c.want == 0) == ok || (ok && d.N != c.want) {
			t.Errorf("At(%v,%v) = %v,%v; want display %d", c.x, c.y, d.N, ok, c.want)
		}
	}
}
