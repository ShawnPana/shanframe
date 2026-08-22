//go:build darwin && cgo

package screencap

/*
#cgo LDFLAGS: -framework AppKit -framework ImageIO
#include <stdlib.h>
#include <stdint.h>
int sfCursorPNG(uint8_t **out, int *outLen, double *hotX, double *hotY, double *w, double *h);
*/
import "C"
import "unsafe"

// Cursor returns the current system cursor: PNG bytes, hotspot and display
// size in points. ok is false when the platform can't say.
func Cursor() (png []byte, hotX, hotY, w, h float64, ok bool) {
	var buf *C.uint8_t
	var n C.int
	var hx, hy, cw, ch C.double
	if C.sfCursorPNG(&buf, &n, &hx, &hy, &cw, &ch) == 0 {
		return nil, 0, 0, 0, 0, false
	}
	defer C.free(unsafe.Pointer(buf))
	return C.GoBytes(unsafe.Pointer(buf), n), float64(hx), float64(hy), float64(cw), float64(ch), true
}
