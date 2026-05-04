package rpc

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression: when the daemon disconnects mid-call, readLoop closes
// every pending channel, which delivers a nil *Response. Prior to the
// fix, Call() unconditionally dereferenced ``r.Error`` and panicked
// the whole TUI for every in-flight RPC. After the fix, the closed
// channel translates to a "daemon disconnected" error so callers can
// surface a toast / retry instead of bringing down the program.
func TestCall_ReturnsErrorWhenDaemonDisconnects(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "chubby-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "f.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// Accept one connection and immediately close it — simulates the
	// daemon dying after the TUI dialed in.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}()

	cl, err := Dial(sock)
	if err != nil {
		ln.Close()
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		cl.Close()
		ln.Close()
	})

	// The Call may race the server's close: if writeFrame fires before
	// the server closes, we get a transport error from writeFrame; if
	// after, we get the "daemon disconnected" error from the closed
	// pending channel. Either way, no panic and a real error.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = cl.Call(ctx, "ping", nil)
	if err == nil {
		t.Fatalf("expected an error from Call after daemon disconnect, got nil")
	}
	// Don't constrain the exact wording — we want either side of the
	// race to pass — but it must NOT be a "deadline exceeded" (which
	// would mean the panic-fix was insufficient and Call hung).
	if strings.Contains(err.Error(), "deadline") {
		t.Fatalf("Call should fail fast on disconnect, not time out; got %v", err)
	}
}

func TestRoundTripFrame(t *testing.T) {
	var buf bytes.Buffer
	body := []byte(`{"a":1}`)
	if err := writeFrameBuf(&buf, body); err != nil {
		t.Fatal(err)
	}
	got, err := readFrameBuf(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("got %q want %q", got, `{"a":1}`)
	}
}

func TestRoundTripFrameEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrameBuf(&buf, []byte{}); err != nil {
		t.Fatal(err)
	}
	got, err := readFrameBuf(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty body, got %q", got)
	}
}

func TestRoundTripFrameLarge(t *testing.T) {
	var buf bytes.Buffer
	body := bytes.Repeat([]byte("x"), 65537)
	if err := writeFrameBuf(&buf, body); err != nil {
		t.Fatal(err)
	}
	got, err := readFrameBuf(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body round-trip mismatch (lens %d/%d)", len(got), len(body))
	}
}

// helpers that use bytes.Buffer so we can test framing without a net.Conn.
func writeFrameBuf(w *bytes.Buffer, body []byte) error {
	hdr := make([]byte, 4)
	hdr[0] = byte(len(body) >> 24)
	hdr[1] = byte(len(body) >> 16)
	hdr[2] = byte(len(body) >> 8)
	hdr[3] = byte(len(body))
	w.Write(hdr)
	w.Write(body)
	return nil
}

func readFrameBuf(r *bytes.Buffer) ([]byte, error) {
	hdr := make([]byte, 4)
	if _, err := r.Read(hdr); err != nil {
		return nil, err
	}
	n := int(hdr[0])<<24 | int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])
	body := make([]byte, n)
	if n == 0 {
		return body, nil
	}
	if _, err := r.Read(body); err != nil {
		return nil, err
	}
	return body, nil
}
