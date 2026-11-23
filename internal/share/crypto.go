// Package share implements germ's session sharing: a host streams its tmux
// session to guests through a relay, end-to-end encrypted with a key that
// only travels inside the invite link's #fragment (browsers never send
// fragments to servers, so the relay never sees it).
package share

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// KeySize is the size of a session key (AES-256).
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

// box seals and opens messages with AES-256-GCM and random nonces.
type box struct{ aead cipher.AEAD }

func newBox(k Key) *box {
	block, err := aes.NewCipher(k[:])
	if err != nil {
		panic(err) // only fails for bad key sizes
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return &box{aead}
}

// seal encrypts msg: nonce || ciphertext.
func (b *box) seal(msg []byte) []byte {
	out := make([]byte, b.aead.NonceSize(), b.aead.NonceSize()+len(msg)+b.aead.Overhead())
	if _, err := rand.Read(out); err != nil {
		panic(err)
	}
	return b.aead.Seal(out, out, msg, nil)
}

var errDecrypt = errors.New("message failed to decrypt (wrong key?)")

func (b *box) open(data []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(data) < n+b.aead.Overhead() {
		return nil, errDecrypt
	}
	msg, err := b.aead.Open(nil, data[:n], data[n:], nil)
	if err != nil {
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
