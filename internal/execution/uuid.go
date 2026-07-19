package execution

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewClientOrderID returns a fresh RFC 4122 version-4 (random) UUID string used
// as an order intent's stable client order id. It draws 16 bytes from
// crypto/rand and sets the version and variant bits, so ids are unique across
// the process without a central allocator. An error is returned only if the
// system entropy source fails.
func NewClientOrderID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("execution: generate client order id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10

	var buf [36]byte
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf[:]), nil
}
