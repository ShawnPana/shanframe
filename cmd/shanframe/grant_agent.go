package main

// The device side of tunnel grants: a session that arrived with grants may
// carry the tunnel's control stream and tcp streams to a granted host:port,
// nothing else.

import (
	"fmt"
	"strings"

	"github.com/shawnpana/shanframe/internal/rendezvous"
)

// grantAllows reports whether a stream request stays inside the grants.
func grantAllows(grants []rendezvous.Grant, open rendezvous.Open) bool {
	switch open.Service {
	case "tunnel": // the session's control stream: carries no bytes of its own
		return true
	case "tcp":
		for _, g := range grants {
			if g.Port == open.Port && sameHost(g.Host, open.Host) {
				return true
			}
		}
	}
	return false
}

// sameHost treats the spellings of "this machine" as one host; anything
// else must match exactly — the grant says where, the device resolves it.
func sameHost(granted, asked string) bool {
	return loopback(granted) && loopback(asked) || strings.EqualFold(granted, asked)
}

func loopback(h string) bool {
	switch strings.ToLower(h) {
	case "", "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

func grantList(grants []rendezvous.Grant) string {
	parts := make([]string, len(grants))
	for i, g := range grants {
		parts[i] = fmt.Sprintf("%s:%d", g.Host, g.Port)
	}
	return strings.Join(parts, ", ")
}
