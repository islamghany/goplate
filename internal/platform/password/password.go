// Package password hashes passwords with argon2id (ADR-0007).
//
// argon2id is OWASP's first recommendation: memory-hard (expensive on GPUs)
// and without bcrypt's silent 72-byte input truncation. Hashes are stored in
// the standard PHC string format, which embeds the parameters, so they can
// be raised later without invalidating existing hashes.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are argon2id cost parameters.
type Params struct {
	Memory  uint32 // KiB
	Time    uint32 // iterations
	Threads uint8
	SaltLen uint32
	KeyLen  uint32
}

// DefaultParams follow the OWASP Password Storage Cheat Sheet
// (m=19 MiB, t=2, p=1).
var DefaultParams = Params{Memory: 19 * 1024, Time: 2, Threads: 1, SaltLen: 16, KeyLen: 32}

// Hasher hashes and verifies passwords.
type Hasher struct {
	p Params
}

// NewHasher returns a Hasher using p.
func NewHasher(p Params) *Hasher { return &Hasher{p: p} }

// Hash returns a PHC-formatted argon2id hash of plain.
func (h *Hasher) Hash(plain string) (string, error) {
	salt := make([]byte, h.p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, h.p.Time, h.p.Memory, h.p.Threads, h.p.KeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.p.Memory, h.p.Time, h.p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Compare reports whether plain matches hash, in constant time.
func (h *Hasher) Compare(hash, plain string) (bool, error) {
	p, salt, key, err := decode(hash)
	if err != nil {
		return false, err
	}
	other := argon2.IDKey([]byte(plain), salt, p.Time, p.Memory, p.Threads, uint32(len(key))) //nolint:gosec // key is a short decoded hash
	return subtle.ConstantTimeCompare(key, other) == 1, nil
}

var errMalformed = errors.New("password: malformed argon2id hash")

func decode(hash string) (Params, []byte, []byte, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return Params{}, nil, nil, errMalformed
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return Params{}, nil, nil, errMalformed
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return Params{}, nil, nil, errMalformed
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	key, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(key) == 0 {
		return Params{}, nil, nil, errMalformed
	}
	return p, salt, key, nil
}
