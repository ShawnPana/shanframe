package android

import (
	"context"
	"net"
	"os"
	"time"
)

// FixNet makes Go's networking work in a bare Android userland, where there
// is no /etc/resolv.conf (Go would try 127.0.0.1:53) and the system CA store
// is somewhere Go doesn't look. Call once at process start, before any dial.
func FixNet() {
	if !Available() {
		return
	}
	if _, err := os.Stat("/etc/resolv.conf"); err != nil {
		d := &net.Dialer{Timeout: 5 * time.Second}
		net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			c, err := d.DialContext(ctx, network, "1.1.1.1:53")
			if err != nil {
				c, err = d.DialContext(ctx, network, "8.8.8.8:53")
			}
			return c, err
		}}
	}
	if os.Getenv("SSL_CERT_FILE") == "" && os.Getenv("SSL_CERT_DIR") == "" {
		// Termux's bundle if present, else Android's own stores (APEX on 14+)
		for _, f := range []string{"/data/data/com.termux/files/usr/etc/tls/cert.pem"} {
			if _, err := os.Stat(f); err == nil {
				os.Setenv("SSL_CERT_FILE", f)
				return
			}
		}
		os.Setenv("SSL_CERT_DIR", "/apex/com.android.conscrypt/cacerts:/system/etc/security/cacerts")
	}
}
