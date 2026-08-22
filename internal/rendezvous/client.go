package rendezvous

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// ErrUnauthorized: the server refused our key (revoked or unknown). Retrying
// is pointless; the caller decides what to tell the user.
var ErrUnauthorized = errors.New("this device's key was refused by the server — it was revoked or never linked; run `shanframe join`")

// Client is one participant's connection to the server, with reconnect.
type Client struct {
	URL   string // ws(s)://host/v1/ws
	Token string
	Hello Msg // sent on every (re)connect
	// OnMsg handles every inbound message; OnConnect fires after hello ack.
	OnMsg          func(Msg)
	OnConnect      func()
	OnUnauthorized func() // key refused; Run returns after calling this

	mu   sync.Mutex
	conn *websocket.Conn
}

// Run keeps the connection alive until ctx ends.
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrUnauthorized) {
			if c.OnUnauthorized != nil {
				c.OnUnauthorized()
			}
			return
		}
		if time.Since(start) > 10*time.Second {
			backoff = time.Second // the connection was healthy; this is a fresh outage
		}
		log.Printf("server: disconnected (%v); retrying in %s", err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) runOnce(ctx context.Context) error {
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, resp, err := websocket.Dial(dctx, c.URL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.Token}},
	})
	cancel()
	if err != nil {
		if (resp != nil && resp.StatusCode == http.StatusUnauthorized) || strings.Contains(err.Error(), "got 401") {
			return ErrUnauthorized
		}
		return err
	}
	conn.SetReadLimit(4 << 20)
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		conn.Close(websocket.StatusNormalClosure, "")
	}()
	if err := c.Send(c.Hello); err != nil {
		return err
	}
	if c.OnConnect != nil {
		c.OnConnect()
	}
	// keepalive
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				pctx, pc := context.WithTimeout(ctx, 10*time.Second)
				conn.Ping(pctx)
				pc()
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var m Msg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if c.OnMsg != nil {
			c.OnMsg(m)
		}
	}
}

// Send writes one message; errors if not connected.
func (c *Client) Send(m Msg) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("not connected to server")
	}
	b, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, b)
}
