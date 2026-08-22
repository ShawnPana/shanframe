//go:build darwin && cgo

// Package power reports when this machine is about to sleep or has woken,
// so the device list can say "asleep" instead of "offline".
package power

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
int sfPowerWatch(void);
*/
import "C"

var handler func(asleep bool)

//export sfPowerEvent
func sfPowerEvent(asleep C.int) {
	if handler != nil {
		handler(asleep != 0)
	}
}

// Watch calls f(true) just before the machine sleeps and f(false) when it
// wakes. Returns false when the OS offers no sleep notices.
func Watch(f func(asleep bool)) bool {
	handler = f
	return C.sfPowerWatch() != 0
}
