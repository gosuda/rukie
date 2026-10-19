package store

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"
)

// ID is a 26-character Crockford base32 ULID. It is the only identifier that leaves the store.
type ID string

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var idGen struct {
	mu   sync.Mutex
	ms   uint64
	rand [10]byte
}

// NewID returns a ULID with now's millisecond timestamp. IDs from one process are strictly
// increasing even within a millisecond and even when the clock steps backward. Times before the
// Unix epoch encode as zero.
func NewID(now time.Time) ID {
	idGen.mu.Lock()
	defer idGen.mu.Unlock()
	if ms := ulidMS(now); ms > idGen.ms {
		idGen.ms = ms
		fillRandom(&idGen.rand)
	} else if !incrementRandom(&idGen.rand) {
		idGen.ms++
		fillRandom(&idGen.rand)
	}
	var raw [16]byte
	putBigEndian48(raw[:6], idGen.ms)
	copy(raw[6:], idGen.rand[:])
	return ID(encodeULID(raw))
}

func fillRandom(b *[10]byte) {
	_, _ = rand.Read(b[:])
}

func incrementRandom(b *[10]byte) bool {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return true
		}
	}
	return false
}

func putBigEndian48(dst []byte, v uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	copy(dst, buf[2:])
}

func encodeULID(raw [16]byte) string {
	var out [26]byte
	var acc uint32
	var bits uint
	j := len(out) - 1
	for i := len(raw) - 1; i >= 0; i-- {
		acc |= uint32(raw[i]) << bits
		bits += 8
		for bits >= 5 {
			out[j] = crockford[acc&0x1f]
			j--
			acc >>= 5
			bits -= 5
		}
	}
	out[j] = crockford[acc&0x1f]
	return string(out[:])
}

func ulidMS(t time.Time) uint64 {
	ms := msOf(t)
	if ms < 0 {
		return 0
	}
	return uint64(ms)
}

func msOf(t time.Time) int64 {
	return t.UnixMilli()
}

func timeOf(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}
