//go:build darwin && cgo

package screencap

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework ImageIO -framework ScreenCaptureKit
#include <stdint.h>
#include <stdlib.h>
int sfStill(int displayN, uint8_t **out, int *n, int *w, int *h);
*/
import "C"

import (
	"errors"
	"unsafe"
)

// Still returns one PNG of display number `display` (0 or 1 = main) at
// point resolution (so pixel coordinates in the image are the coordinates
// input events take).
func Still(display int) (png []byte, w, h int, err error) {
	if _, err := Pick(Displays(), display); err != nil {
		return nil, 0, 0, err
	}
	var out *C.uint8_t
	var n, cw, ch C.int
	if C.sfStill(C.int(display), &out, &n, &cw, &ch) == 0 {
		return nil, 0, 0, errors.New("screen capture failed")
	}
	defer C.free(unsafe.Pointer(out))
	return C.GoBytes(unsafe.Pointer(out), n), int(cw), int(ch), nil
}
