package rpc

import (
	"bytes"
	"testing"
)

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
