package main

import (
	"testing"

	"github.com/shawnpana/shanframe/internal/rendezvous"
)

// A granted session gets the tunnel control stream and tcp to the granted
// port, and nothing else: no shell, no other port, no other host.
func TestGrantAllowsOnlyTheGrantedPort(t *testing.T) {
	grants := []rendezvous.Grant{{Host: "127.0.0.1", Port: 7777}}
	yes := []rendezvous.Open{
		{Service: "tunnel"},
		{Service: "tcp", Host: "127.0.0.1", Port: 7777},
		{Service: "tcp", Host: "localhost", Port: 7777},
		{Service: "tcp", Host: "", Port: 7777},
	}
	no := []rendezvous.Open{
		{Service: "shell"},
		{Service: "exec", Cmd: "id"},
		{Service: "screen"},
		{Service: "screenshot"},
		{Service: "info"},
		{Service: "tcp", Host: "127.0.0.1", Port: 22},
		{Service: "tcp", Host: "10.0.0.5", Port: 7777},
		{Service: "tcp", Host: "example.com", Port: 7777},
		{Service: "tcp"},
	}
	for _, o := range yes {
		if !grantAllows(grants, o) {
			t.Errorf("%+v must be allowed", o)
		}
	}
	for _, o := range no {
		if grantAllows(grants, o) {
			t.Errorf("%+v must be refused", o)
		}
	}
	if grantAllows(nil, rendezvous.Open{Service: "tcp", Host: "localhost", Port: 7777}) {
		t.Error("no grants allow nothing")
	}
	named := []rendezvous.Grant{{Host: "db.internal", Port: 5432}}
	if !grantAllows(named, rendezvous.Open{Service: "tcp", Host: "DB.internal", Port: 5432}) {
		t.Error("a named host matches case-insensitively")
	}
	if grantAllows(named, rendezvous.Open{Service: "tcp", Host: "localhost", Port: 5432}) {
		t.Error("a named host is not loopback")
	}
}
