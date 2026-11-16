package strkey

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"testing"
)

// Vectors verified against py-stellar-base (stellar-sdk) on 2026-11-15.
const (
	vectorSeed    = "SDHOAMBNLGCE2MV5ZKIVZAQD3VCLGP53P3OBSBI6UN5L5XZI5TKHFQL4"
	vectorAddress = "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H"
	// Raw seed bytes 0x00..0x1f.
	seqSeed    = "SAAACAQDAQCQMBYIBEFAWDANBYHRAEISCMKBKFQXDAMRUGY4DUPB6NKI"
	seqAddress = "GAB2CB576PHBBPQ5ODORRZ2LYCMWPZGWGCN2KDK7DXOIMZASKUY3QZ6Q"
)

func TestSequentialSeedVector(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	seed, err := Encode(VersionSeed, raw)
	if err != nil || seed != seqSeed {
		t.Fatalf("Encode(seed) = %s, want %s (%v)", seed, seqSeed, err)
	}
	addr, err := PublicKeyFromSeed([]byte(seed))
	if err != nil || addr != seqAddress {
		t.Fatalf("PublicKeyFromSeed = %s, want %s (%v)", addr, seqAddress, err)
	}
}

func TestCRC16XModem(t *testing.T) {
	// The standard check value for CRC-16/XMODEM.
	if got := crc16([]byte("123456789")); got != 0x31C3 {
		t.Fatalf("crc16 = %#04x, want 0x31c3", got)
	}
}

func TestKnownVector(t *testing.T) {
	got, err := PublicKeyFromSeed([]byte(vectorSeed))
	if err != nil {
		t.Fatal(err)
	}
	if got != vectorAddress {
		t.Errorf("PublicKeyFromSeed = %s, want %s", got, vectorAddress)
	}
	if !ValidPublicKey(vectorAddress) {
		t.Error("vector address rejected")
	}
}

func TestRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := Encode(VersionPublicKey, pub)
	if err != nil {
		t.Fatal(err)
	}
	if addr[0] != 'G' || len(addr) != EncodedLen {
		t.Fatalf("address %q", addr)
	}
	back, err := DecodePublicKey(addr)
	if err != nil || !bytes.Equal(back, pub) {
		t.Fatalf("decode: %v", err)
	}
	seed, err := Encode(VersionSeed, priv.Seed())
	if err != nil {
		t.Fatal(err)
	}
	if seed[0] != 'S' {
		t.Fatalf("seed %q", seed[:1])
	}
	derived, err := PublicKeyFromSeed([]byte(seed))
	if err != nil || derived != addr {
		t.Fatalf("derived %s, want %s (%v)", derived, addr, err)
	}
}

func TestRejects(t *testing.T) {
	// flip replaces the character at i with a different valid base32 character.
	flip := func(s string, i int) string {
		b := []byte(s)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		return string(b)
	}
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", ErrLength},
		{"short", vectorAddress[:55], ErrLength},
		{"long", vectorAddress + "A", ErrLength},
		{"lowercase", "g" + vectorAddress[1:], ErrEncoding},
		{"bad char", vectorAddress[:10] + "1" + vectorAddress[11:], ErrEncoding},
		{"payload changed", flip(vectorAddress, 20), ErrChecksum},
		{"checksum changed", flip(vectorAddress, 54), ErrChecksum},
		{"seed as address", vectorSeed, ErrVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodePublicKey(tt.in)
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
	if _, err := DecodeSeed([]byte(vectorAddress)); !errors.Is(err, ErrVersion) {
		t.Errorf("address accepted as seed: %v", err)
	}
}

func TestAbbrev(t *testing.T) {
	if got := Abbrev(vectorAddress); got != "GBRP…OX2H" {
		t.Errorf("Abbrev = %q", got)
	}
}

func FuzzDecodePublicKey(f *testing.F) {
	f.Add(vectorAddress)
	f.Add(vectorSeed)
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		payload, err := DecodePublicKey(s)
		if err != nil {
			return
		}
		again, err := Encode(VersionPublicKey, payload)
		if err != nil || again != s {
			t.Fatalf("accepted %q but re-encodes to %q (%v)", s, again, err)
		}
	})
}
