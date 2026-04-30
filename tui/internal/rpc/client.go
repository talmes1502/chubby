// Package rpc is a JSON-RPC 2.0 client over a length-prefixed (4-byte BE)
// Unix-socket transport. Mirrors the Python proto in src/chubby/proto/.
package rpc

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
)

// MaxFrameSize matches the Python framer cap.
const MaxFrameSize = 16 * 1024 * 1024

type Request struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int64          `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type Event struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

type RPCError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Client is a JSON-RPC 2.0 client. The Events channel receives server-pushed
// Event frames. Disconnects are signaled via the Disconnected() channel,
// which closes whenever the read loop exits due to a transport error;
// Reconnect() re-arms it.
type Client struct {
	mu           sync.Mutex
	conn         net.Conn
	br           *bufio.Reader
	pending      map[int64]chan *Response
	events       chan Event
	idGen        atomic.Int64
	sockPath     string
	disconnected chan struct{}
}

// Dial connects to chubbyd at sockPath and starts a read loop.
func Dial(sockPath string) (*Client, error) {
	c, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, err
	}
	cl := &Client{
		conn:         c,
		br:           bufio.NewReader(c),
		pending:      map[int64]chan *Response{},
		events:       make(chan Event, 1024),
		sockPath:     sockPath,
		disconnected: make(chan struct{}),
	}
	go cl.readLoop()
	return cl, nil
}

// Disconnected returns a channel that closes when the read loop exits
// due to a transport error. Call Reconnect to re-arm it.
func (c *Client) Disconnected() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.disconnected
}

// Call performs a synchronous JSON-RPC call, blocked on ctx.
func (c *Client) Call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	id := c.idGen.Add(1)
	if params == nil {
		params = map[string]any{}
	}
	req := Request{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	ch := make(chan *Response, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	if err := writeFrame(c.conn, body); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case r := <-ch:
		if r.Error != nil {
			return nil, r.Error
		}
		return r.Result, nil
	}
}

// Events returns the receive-only channel of server-pushed events.
func (c *Client) Events() <-chan Event { return c.events }

// Close closes the underlying connection.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Reconnect reopens the unix socket and replaces the read loop. Pending calls
// from the previous connection are dropped.
func (c *Client) Reconnect() error {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	conn, err := net.Dial("unix", c.sockPath)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.br = bufio.NewReader(conn)
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.disconnected = make(chan struct{})
	c.mu.Unlock()
	go c.readLoop()
	return nil
}

func (c *Client) readLoop() {
	for {
		body, err := readFrame(c.br)
		if err != nil {
			c.mu.Lock()
			// Wake any pending callers so they get a context deadline rather than
			// a deadlock; we close the channels which delivers a nil *Response.
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			// Signal disconnect; Reconnect re-arms it. Skip if already closed.
			select {
			case <-c.disconnected:
				// already closed
			default:
				close(c.disconnected)
			}
			c.mu.Unlock()
			// Don't close c.events on a single transient read error — Reconnect
			// will spawn a fresh readLoop. Just return.
			return
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(body, &probe); err != nil {
			continue
		}
		if _, hasID := probe["id"]; hasID {
			var r Response
			if err := json.Unmarshal(body, &r); err == nil {
				c.mu.Lock()
				ch, ok := c.pending[r.ID]
				delete(c.pending, r.ID)
				c.mu.Unlock()
				if ok {
					ch <- &r
				}
			}
			continue
		}
		var ev Event
		if err := json.Unmarshal(body, &ev); err == nil && ev.Method != "" {
			select {
			case c.events <- ev:
			default: // drop on overflow
			}
		}
	}
}

func writeFrame(w net.Conn, body []byte) error {
	if len(body) > MaxFrameSize {
		return fmt.Errorf("frame too large")
	}
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(body)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

func readFrame(br *bufio.Reader) ([]byte, error) {
	hdr := make([]byte, 4)
	if _, err := bufReadFull(br, hdr); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr)
	if n > MaxFrameSize {
		return nil, fmt.Errorf("frame too large: %d", n)
	}
	body := make([]byte, n)
	if _, err := bufReadFull(br, body); err != nil {
		return nil, err
	}
	return body, nil
}

func bufReadFull(br *bufio.Reader, p []byte) (int, error) {
	read := 0
	for read < len(p) {
		n, err := br.Read(p[read:])
		read += n
		if err != nil {
			return read, err
		}
	}
	return read, nil
}
