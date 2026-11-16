// Package strkey encodes and validates Stellar StrKeys: public keys (G…) and
// secret seeds (S…).
//
// A StrKey is base32 (RFC 4648, no padding) of a version byte, a 32-byte
// payload and a CRC16-XModem checksum stored little-endian. Seeds are handled
// only as []byte so callers can zero them (CLAUDE.md hard rule 1).
package strkey

import (
	"crypto/ed25519"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
)

// VersionByte identifies the kind of key.
type VersionByte byte

const (
	// VersionPublicKey is an ed25519 public key ("G…").
	VersionPublicKey VersionByte = 6 << 3
	// VersionSeed is an ed25519 secret seed ("S…").
	VersionSeed VersionByte = 18 << 3
)

const (
	payloadLen = 32
	rawLen     = 1 + payloadLen + 2
	// EncodedLen is the length of an encoded key. 35 bytes are exactly 280
	// bits, so every 56-character base32 string decodes canonically.
	EncodedLen = 56
)

var enc = base32.StdEncoding.WithPadding(base32.NoPadding)

// Errors returned by the decoders.
var (
	ErrLength   = errors.New("strkey: wrong length")
	ErrEncoding = errors.New("strkey: not valid base32")
	ErrVersion  = errors.New("strkey: wrong key type")
	ErrChecksum = errors.New("strkey: checksum mismatch")
)

// Encode returns the StrKey for a 32-byte payload.
func Encode(v VersionByte, payload []byte) (string, error) {
	if len(payload) != payloadLen {
		return "", fmt.Errorf("%w: payload is %d bytes, want %d", ErrLength, len(payload), payloadLen)
	}
	raw := make([]byte, 0, rawLen)
	raw = append(raw, byte(v))
	raw = append(raw, payload...)
	raw = binary.LittleEndian.AppendUint16(raw, crc16(raw))
	out := make([]byte, enc.EncodedLen(len(raw)))
	enc.Encode(out, raw)
	clear(raw)
	return string(out), nil
}

// decode checks src and returns the payload. The returned slice is new memory
// the caller owns.
func decode(want VersionByte, src []byte) ([]byte, error) {
	if len(src) != EncodedLen {
		return nil, ErrLength
	}
	raw := make([]byte, enc.DecodedLen(len(src)))
	defer clear(raw)
	n, err := enc.Decode(raw, src)
	if err != nil || n != rawLen {
		return nil, ErrEncoding
	}
	if VersionByte(raw[0]) != want {
		return nil, ErrVersion
	}
	if crc16(raw[:rawLen-2]) != binary.LittleEndian.Uint16(raw[rawLen-2:]) {
		return nil, ErrChecksum
	}
	payload := make([]byte, payloadLen)
	copy(payload, raw[1:1+payloadLen])
	return payload, nil
}

// DecodePublicKey returns the 32-byte ed25519 key of a "G…" address.
func DecodePublicKey(s string) ([]byte, error) {
	return decode(VersionPublicKey, []byte(s))
}

// ValidPublicKey reports whether s is a well-formed "G…" address.
func ValidPublicKey(s string) bool {
	_, err := DecodePublicKey(s)
	return err == nil
}

// DecodeSeed returns the 32-byte raw seed of an "S…" secret seed. The caller
// must clear() both the input and the result when done.
func DecodeSeed(seed []byte) ([]byte, error) {
	return decode(VersionSeed, seed)
}

// PublicKeyFromSeed derives the "G…" address of an "S…" seed. The seed is not
// modified; intermediate key material is cleared.
func PublicKeyFromSeed(seed []byte) (string, error) {
	raw, err := DecodeSeed(seed)
	if err != nil {
		return "", err
	}
	defer clear(raw)
	priv := ed25519.NewKeyFromSeed(raw)
	defer clear(priv)
	return Encode(VersionPublicKey, priv[32:])
}

// Abbrev shortens an address for display: "GABC…WXYZ".
func Abbrev(address string) string {
	if len(address) <= 9 {
		return address
	}
	return address[:4] + "…" + address[len(address)-4:]
}

// crc16 is CRC16-XModem (polynomial 0x1021, initial value 0).
func crc16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
