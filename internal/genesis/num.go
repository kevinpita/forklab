package genesis

import (
	"fmt"
	"math/big"
	"strings"
)

// decPrecision is the number of fractional digits of an SDK LegacyDec.
const decPrecision = 18

var decUnit = new(big.Int).Exp(big.NewInt(10), big.NewInt(decPrecision), nil)

// parseInt parses an SDK Int string such as "20000000".
func parseInt(s string) (*big.Int, error) {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("invalid integer %q", s)
	}
	return n, nil
}

// parseDec parses an SDK LegacyDec string such as "20000000.000000000000000000"
// into an integer scaled by 1e18.
func parseDec(s string) (*big.Int, error) {
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > decPrecision {
		return nil, fmt.Errorf("invalid decimal %q: more than %d fractional digits", s, decPrecision)
	}
	n, ok := new(big.Int).SetString(whole+frac+strings.Repeat("0", decPrecision-len(frac)), 10)
	if !ok || strings.HasPrefix(s, "-") {
		return nil, fmt.Errorf("invalid decimal %q", s)
	}
	return n, nil
}

// formatDec renders a 1e18-scaled integer as an SDK LegacyDec string.
func formatDec(n *big.Int) string {
	whole, frac := new(big.Int).QuoRem(n, decUnit, new(big.Int))
	return fmt.Sprintf("%s.%018s", whole, frac)
}

// intToDec scales a token amount to a LegacyDec.
func intToDec(n *big.Int) *big.Int {
	return new(big.Int).Mul(n, decUnit)
}

// ceilDiv returns a/b rounded up.
func ceilDiv(a, b *big.Int) *big.Int {
	q, r := new(big.Int).QuoRem(a, b, new(big.Int))
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

// roundUp returns the smallest multiple of unit that is >= n.
func roundUp(n, unit *big.Int) *big.Int {
	return new(big.Int).Mul(ceilDiv(n, unit), unit)
}
