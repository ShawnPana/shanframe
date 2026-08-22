// Package frame is the tiny wire protocol shanframe speaks over a mesh
// connection: [type:1][len:4 big-endian][payload].
package frame

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	Data   byte = 0 // raw terminal bytes
	Resize byte = 1 // payload: uint16 cols, uint16 rows
	Hello  byte = 2 // client → server, payload: JSON Hello
	Error  byte = 3 // server → client, payload: UTF-8 message
	Stderr byte = 4 // exec: process stderr (Data carries stdout)
	Exit   byte = 5 // exec: process ended, payload: uint32 exit code
	Eof    byte = 6 // exec, client → agent: stdin is closed
)

const maxFrame = 1 << 20

// Write sends one frame as a single Write call, so message-oriented
// transports (WebRTC DataChannels) carry exactly one frame per message.
func Write(w io.Writer, typ byte, payload []byte) error {
	buf := make([]byte, 5+len(payload))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:], uint32(len(payload)))
	copy(buf[5:], payload)
	_, err := w.Write(buf)
	return err
}

func Read(r io.Reader) (typ byte, payload []byte, err error) {
	var hdr [5]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrame {
		return 0, nil, errors.New("frame too large")
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(r, payload)
	return hdr[0], payload, err
}

func ResizePayload(cols, rows int) []byte {
	var b [4]byte
	binary.BigEndian.PutUint16(b[0:], uint16(cols))
	binary.BigEndian.PutUint16(b[2:], uint16(rows))
	return b[:]
}

func ParseResize(b []byte) (cols, rows int, ok bool) {
	if len(b) != 4 {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint16(b[0:])), int(binary.BigEndian.Uint16(b[2:])), true
}
