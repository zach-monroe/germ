// Package share implements germ's session sharing: a host streams its tmux
// session to guests through a relay, end-to-end encrypted with a key that
// only travels inside the invite link's #fragment (browsers never send
// fragments to servers, so the relay never sees it).
package share

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/nacl/secretbox"
)

// KeySize is the size of a session key.
const KeySize = 32

// Key is a symmetric session key.
type Key [KeySize]byte

// NewKey makes a random key.
func NewKey() (Key, error) {
	var k Key
	_, err := rand.Read(k[:])
	return k, err
}

// String encodes the key for a link fragment.
func (k Key) String() string { return base64.RawURLEncoding.EncodeToString(k[:]) }

// ParseKey decodes a key from a link fragment.
func ParseKey(s string) (Key, error) {
	var k Key
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != KeySize {
		return k, fmt.Errorf("bad key in link")
	}
	copy(k[:], b)
	return k, nil
}

// box seals and opens messages with NaCl secretbox (XSalsa20-Poly1305)
// and random nonces: nonce (24 bytes) || box. Browsers can run the same
// construction in plain JavaScript (tweetnacl), which matters because Web
// Crypto only exists on https pages and direct shares are plain http.
type box struct{ key [KeySize]byte }

func newBox(k Key) *box { return &box{key: k} }

const nonceSize = 24

// seal encrypts msg.
func (b *box) seal(msg []byte) []byte {
	var nonce [nonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		panic(err)
	}
	return secretbox.Seal(nonce[:], msg, &nonce, &b.key)
}

var errDecrypt = errors.New("message failed to decrypt (wrong key?)")

func (b *box) open(data []byte) ([]byte, error) {
	if len(data) < nonceSize+secretbox.Overhead {
		return nil, errDecrypt
	}
	var nonce [nonceSize]byte
	copy(nonce[:], data)
	msg, ok := secretbox.Open(nil, data[nonceSize:], &nonce, &b.key)
	if !ok {
		return nil, errDecrypt
	}
	return msg, nil
}

// randomID makes a URL-safe random identifier.
func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
