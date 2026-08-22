//go:build !linux && !darwin

package setup

func EnsureScreen() Screen {
	if vncListening() {
		return Screen{Ready: true}
	}
	return Screen{Note: "no screen server on this device"}
}
