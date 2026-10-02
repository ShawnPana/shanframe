package screencap

import "fmt"

// Display is one attached display, numbered the way people count them: the
// main display is 1, then the others left to right (top to bottom for ties).
// W, H are its size in points — the coordinate space of screenshots and of
// the positions input events are given in; X, Y place it in the global
// desktop so input can be aimed at it.
type Display struct {
	N    int     `json:"n"`
	Name string  `json:"name"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Main bool    `json:"main"`
}

// Pick resolves a controller's display number against the list. 0 means the
// main display; a number the device doesn't have is an error in plain words.
func Pick(displays []Display, n int) (Display, error) {
	if n <= 0 {
		n = 1
	}
	if len(displays) == 0 {
		return Display{}, fmt.Errorf("this device has no display to capture")
	}
	if n > len(displays) {
		return Display{}, fmt.Errorf("no display %d (this device has %d)", n, len(displays))
	}
	return displays[n-1], nil
}
