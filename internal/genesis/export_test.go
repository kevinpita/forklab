package genesis

// Valcons exposes the bech32 consensus address of an ed25519 key to the
// behavior tests, which check the rewrite against sha256(pubkey)[:20].
func Valcons(hrp, pubKeyB64 string) string {
	addr, err := consAddress(pubKeyB64)
	if err != nil {
		panic(err)
	}
	return bech32Encode(hrp, addr)
}

// Bech32Decode and Bech32Encode expose the codec so the tests can verify it
// against real addresses in the fixtures and build valid test addresses.
var (
	Bech32Decode = bech32Decode
	Bech32Encode = bech32Encode
)
