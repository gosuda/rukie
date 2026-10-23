package ruk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	FrameBytes        = 13
	HeaderBytes       = 9
	MaxDataPayload    = 64 << 10
	MaxControlPayload = 16 << 20
	ControlStream     = 0
)

type Kind uint8

const (
	KindOpen Kind = iota + 1
	KindAccepted
	KindStdin
	KindStdinEOF
	KindStdout
	KindStderr
	KindWindowUpdate
	KindCancel
	KindExit      = 10
	KindError     = 11
	KindHookCall  = 12
	KindHookReply = 13
)

func (k Kind) IsData() bool {
	switch k {
	case KindStdin, KindStdout, KindStderr:
		return true
	case KindOpen, KindAccepted, KindStdinEOF, KindWindowUpdate, KindCancel,
		KindExit, KindError, KindHookCall, KindHookReply:
		return false
	default:
		return false
	}
}

func (k Kind) bound() int {
	if k.IsData() {
		return MaxDataPayload
	}
	return MaxControlPayload
}

func (k Kind) valid() bool {
	switch k {
	case KindOpen, KindAccepted, KindStdin, KindStdinEOF,
		KindStdout, KindStderr, KindWindowUpdate, KindCancel,
		KindExit, KindError, KindHookCall, KindHookReply:
		return true
	default:
		return false
	}
}

// PutHeader writes one frame header into dst.
func PutHeader(dst *[FrameBytes]byte, k Kind, stream uint64, payloadLen int) error {
	if !k.valid() {
		return fmt.Errorf("PutHeader: %w: kind %d", ErrProtocol, uint8(k))
	}
	if payloadLen < 0 || payloadLen > k.bound() {
		return fmt.Errorf("PutHeader: %w: kind %d length %d", ErrProtocol, uint8(k), payloadLen)
	}
	if stream == ControlStream && (k == KindStdin || k == KindStdout || k == KindStderr) {
		return fmt.Errorf("PutHeader: %w: data frame on stream 0", ErrProtocol)
	}
	total := int64(HeaderBytes) + int64(payloadLen)
	if total < 0 || total > math.MaxUint32 {
		return fmt.Errorf("PutHeader: %w: length %d", ErrProtocol, payloadLen)
	}
	binary.BigEndian.PutUint32(dst[0:4], uint32(total))
	dst[4] = uint8(k)
	binary.BigEndian.PutUint64(dst[5:13], stream)
	return nil
}

// ParseHeader decodes one frame header.
func ParseHeader(src *[FrameBytes]byte) (Kind, uint64, int, error) {
	total := binary.BigEndian.Uint32(src[0:4])
	k := Kind(src[4])
	stream := binary.BigEndian.Uint64(src[5:13])
	if total < HeaderBytes {
		return 0, 0, 0, fmt.Errorf("ParseHeader: %w: total %d", ErrProtocol, total)
	}
	if !k.valid() {
		return 0, 0, 0, fmt.Errorf("ParseHeader: %w: kind %d", ErrProtocol, uint8(k))
	}
	payloadLen := int(total - HeaderBytes)
	if payloadLen > k.bound() {
		return 0, 0, 0, fmt.Errorf("ParseHeader: %w: kind %d length %d", ErrProtocol, uint8(k), payloadLen)
	}
	return k, stream, payloadLen, nil
}

type Frame struct {
	Payload []byte
	Stream  uint64
	Kind    Kind
}

func writeFrame(w io.Writer, k Kind, stream uint64, payload []byte) error {
	var hdr [FrameBytes]byte
	if err := PutHeader(&hdr, k, stream, len(payload)); err != nil {
		return err
	}
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("writeFrame: %w", err)
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return fmt.Errorf("writeFrame: %w", err)
		}
	}
	return nil
}

func readFrame(r io.Reader) (Frame, error) {
	var hdr [FrameBytes]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Frame{}, fmt.Errorf("readFrame: %w: %w", ErrClosed, err)
		}
		return Frame{}, fmt.Errorf("readFrame: %w", err)
	}
	k, stream, payloadLen, err := ParseHeader(&hdr)
	if err != nil {
		return Frame{}, err
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Frame{}, fmt.Errorf("readFrame: %w: %w", ErrClosed, err)
	}
	return Frame{Kind: k, Stream: stream, Payload: payload}, nil
}
