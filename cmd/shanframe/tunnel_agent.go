package main

// tcp service (agent side): one stream = one TCP connection, dialed from
// this device, so a controller reaches anything this device can reach. Same
// trust as the shell it already has here.

import (
	"io"
	"log"
	"net"
	"strconv"
	"time"
)

func serveTCP(s io.ReadWriteCloser, host string, port int) {
	if host == "" {
		host = "localhost"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		log.Printf("tcp %s: %v", addr, err)
		s.Write(append([]byte{1}, []byte(err.Error())...))
		return
	}
	defer conn.Close()
	if _, err := s.Write([]byte{0}); err != nil {
		return
	}
	pipe(s, conn)
}

// pipe copies both ways until either side ends, then closes both.
func pipe(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
	a.Close()
	b.Close()
	<-done
}
