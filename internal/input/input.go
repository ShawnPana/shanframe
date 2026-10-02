package input

// Rect is the display an Injector aims at: its origin in the global desktop
// and its size, in points. Normalized event positions (0..1) map onto it, so
// a controller watching display 2 clicks on display 2.
type Rect struct{ X, Y, W, H float64 }
