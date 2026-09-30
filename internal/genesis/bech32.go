package genesis

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const consAddrLen = 20

// consAddress is the CometBFT address of an ed25519 key: sha256(key)[:20].
func consAddress(pubKeyB64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil {
		return nil, fmt.Errorf("consensus key %q: %w", pubKeyB64, err)
	}
	sum := sha256.Sum256(raw)
	return sum[:consAddrLen], nil
}

// Minimal BIP173 bech32, enough to move 20-byte addresses between prefixes.

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

var bech32Generator = [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}

func bech32Polymod(values []byte) uint32 {
	chk := uint32(1)
	for _, v := range values {
		b := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := range 5 {
			if (b>>i)&1 == 1 {
				chk ^= bech32Generator[i]
			}
		}
	}
	return chk
}

func bech32HRPExpand(hrp string) []byte {
	out := make([]byte, 0, 2*len(hrp)+1)
	for i := range len(hrp) {
		out = append(out, hrp[i]>>5)
	}
	out = append(out, 0)
	for i := range len(hrp) {
		out = append(out, hrp[i]&31)
	}
	return out
}

// convertBits regroups a byte string from `from` bits per byte to `to` bits.
func convertBits(data []byte, from, to uint, pad bool) ([]byte, error) {
	var acc, bits uint
	var out []byte
	maxv := uint(1)<<to - 1
	for _, b := range data {
		acc = acc<<from | uint(b)
		bits += from
		for bits >= to {
			bits -= to
			out = append(out, byte(acc>>bits&maxv))
		}
	}
	if pad {
		if bits > 0 {
			out = append(out, byte(acc<<(to-bits)&maxv))
		}
	} else if bits >= from || acc<<(to-bits)&maxv != 0 {
		return nil, errors.New("invalid padding")
	}
	return out, nil
}

// bech32Encode encodes data (8-bit bytes) under hrp.
func bech32Encode(hrp string, data []byte) string {
	data5, _ := convertBits(data, 8, 5, true)
	values := append(bech32HRPExpand(hrp), data5...)
	polymod := bech32Polymod(append(values, 0, 0, 0, 0, 0, 0)) ^ 1
	var sb strings.Builder
	sb.WriteString(hrp)
	sb.WriteByte('1')
	for _, d := range data5 {
		sb.WriteByte(bech32Charset[d])
	}
	for i := range 6 {
		sb.WriteByte(bech32Charset[(polymod>>uint(5*(5-i)))&31])
	}
	return sb.String()
}

// bech32Decode returns the hrp and the 8-bit data of a bech32 string.
func bech32Decode(s string) (string, []byte, error) {
	if strings.ToLower(s) != s {
		return "", nil, fmt.Errorf("bech32 %q: mixed case", s)
	}
	pos := strings.LastIndexByte(s, '1')
	if pos < 1 || pos+7 > len(s) {
		return "", nil, fmt.Errorf("bech32 %q: invalid separator position", s)
	}
	hrp := s[:pos]
	data5 := make([]byte, 0, len(s)-pos-1)
	for _, c := range s[pos+1:] {
		idx := strings.IndexRune(bech32Charset, c)
		if idx < 0 {
			return "", nil, fmt.Errorf("bech32 %q: invalid character %q", s, c)
		}
		data5 = append(data5, byte(idx))
	}
	if bech32Polymod(append(bech32HRPExpand(hrp), data5...)) != 1 {
		return "", nil, fmt.Errorf("bech32 %q: bad checksum", s)
	}
	data, err := convertBits(data5[:len(data5)-6], 5, 8, false)
	if err != nil {
		return "", nil, fmt.Errorf("bech32 %q: %w", s, err)
	}
	return hrp, data, nil
}
