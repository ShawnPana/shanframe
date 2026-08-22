//go:build !darwin || !cgo

package power

// Watch is a no-op where the OS gives no sleep notices; such machines just
// go offline.
func Watch(f func(asleep bool)) bool { return false }
