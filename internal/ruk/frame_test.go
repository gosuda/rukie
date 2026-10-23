package ruk

import (
	"bytes"
	"net"
	"testing"
)

func TestRukClientFramingRoundTrip(t *testing.T) {
	kinds := []Kind{
		KindOpen, KindAccepted, KindStdin, KindStdinEOF,
		KindStdout, KindStderr, KindWindowUpdate, KindCancel,
		KindExit, KindError, KindHookCall, KindHookReply,
	}
	client, server := net.Pipe()
	defer func() {
		_ = client.Close()
	}()
	defer func() {
		_ = server.Close()
	}()
	go func() {
		for _, k := range kinds {
			payload := []byte("payload-" + string(rune('a'+uint8(k))))
			if err := writeFrame(server, k, uint64(k)+100, payload); err != nil {
				return
			}
		}
	}()
	for _, k := range kinds {
		frame, err := readFrame(client)
		if err != nil {
			t.Fatalf("read kind %d: %v", uint8(k), err)
		}
		if frame.Kind != k {
			t.Fatalf("kind = %d, want %d", uint8(frame.Kind), uint8(k))
		}
		if frame.Stream != uint64(k)+100 {
			t.Fatalf("stream = %d, want %d", frame.Stream, uint64(k)+100)
		}
		want := "payload-" + string(rune('a'+uint8(k)))
		if string(frame.Payload) != want {
			t.Fatalf("payload = %q, want %q", frame.Payload, want)
		}
	}
}

func TestFramingBoundaries(t *testing.T) {
	var hdr [FrameBytes]byte
	if err := PutHeader(&hdr, KindStdout, 1, MaxDataPayload); err != nil {
		t.Fatalf("64KiB data rejected: %v", err)
	}
	if err := PutHeader(&hdr, KindStdout, 1, MaxDataPayload+1); err == nil {
		t.Fatal("64KiB+1 data accepted")
	}
	oneMiB := make([]byte, 1<<20)
	if err := writeFrame(ioNop{}, KindExit, 1, oneMiB); err != nil {
		t.Fatalf("1MiB control rejected: %v", err)
	}
	if err := PutHeader(&hdr, KindOpen, 1, MaxControlPayload+1); err == nil {
		t.Fatal("control over cap accepted")
	}
	if err := PutHeader(&hdr, Kind(9), 1, 0); err == nil {
		t.Fatal("retired kind 9 accepted")
	}
	if err := PutHeader(&hdr, Kind(14), 1, 0); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if err := PutHeader(&hdr, KindStdout, ControlStream, 1); err == nil {
		t.Fatal("data frame on stream 0 accepted")
	}
	short := [FrameBytes]byte{0, 0, 0, 8, 1, 0, 0, 0, 0, 0, 0, 0, 0}
	if _, _, _, err := ParseHeader(&short); err == nil {
		t.Fatal("total below header accepted")
	}
	kind9 := [FrameBytes]byte{0, 0, 0, 9, 9, 0, 0, 0, 0, 0, 0, 0, 1}
	if _, _, _, err := ParseHeader(&kind9); err == nil {
		t.Fatal("kind 9 parsed")
	}
	unknown := [FrameBytes]byte{0, 0, 0, 9, 200, 0, 0, 0, 0, 0, 0, 0, 1}
	if _, _, _, err := ParseHeader(&unknown); err == nil {
		t.Fatal("unknown kind parsed")
	}
	over := [FrameBytes]byte{1, 0, 0, 10, 2, 0, 0, 0, 0, 0, 0, 0, 1}
	if _, _, _, err := ParseHeader(&over); err == nil {
		t.Fatal("control over cap parsed without payload read")
	}
}

func TestFramingShortRead(t *testing.T) {
	client, server := net.Pipe()
	defer func() {
		_ = client.Close()
	}()
	go func() {
		var hdr [FrameBytes]byte
		_ = PutHeader(&hdr, KindExit, 7, 4)
		_, _ = server.Write(hdr[:])
		_, _ = server.Write([]byte{1, 2})
		_ = server.Close()
	}()
	if _, err := readFrame(client); err == nil {
		t.Fatal("truncated payload accepted")
	}
}

func TestFramingZeroAlloc(t *testing.T) {
	var hdr [FrameBytes]byte
	if n := testing.AllocsPerRun(100, func() {
		_ = PutHeader(&hdr, KindExit, 9, 16)
	}); n != 0 {
		t.Fatalf("PutHeader allocs = %v", n)
	}
	full := [FrameBytes]byte{0, 0, 0, 25, 10, 0, 0, 0, 0, 0, 0, 0, 9}
	if n := testing.AllocsPerRun(100, func() {
		_, _, _, _ = ParseHeader(&full)
	}); n != 0 {
		t.Fatalf("ParseHeader allocs = %v", n)
	}
}

func TestFramingLargeControlRoundTrip(t *testing.T) {
	big := bytes.Repeat([]byte{7}, 1<<20)
	client, server := net.Pipe()
	defer func() {
		_ = client.Close()
	}()
	defer func() {
		_ = server.Close()
	}()
	go func() {
		_ = writeFrame(server, KindOpen, 3, big)
	}()
	frame, err := readFrame(client)
	if err != nil {
		t.Fatalf("read 1MiB control: %v", err)
	}
	if len(frame.Payload) != len(big) {
		t.Fatalf("payload len = %d, want %d", len(frame.Payload), len(big))
	}
}

type ioNop struct{}

func (ioNop) Write(p []byte) (int, error) {
	return len(p), nil
}
