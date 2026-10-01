package chain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProposalDraftPreservesLegacyPayloadAndNumbers(t *testing.T) {
	raw := []byte(`{"proposal":{"id":"21","title":"Validator","summary":"Full summary","metadata":"ipfs://CID","messages":[{"type":"/poa.MsgAddValidator","value":{"pubkey":{"type":"/cosmos.crypto.ed25519.PubKey","value":"VX8="},"power":9007199254740993,"nested":{"unknown":[18446744073709551615]}}}],"total_deposit":[{"denom":"stake","amount":"500"}]}}`)
	p, err := parseProposal(raw)
	if err != nil {
		t.Fatal(err)
	}
	d, err := p.Draft("10stake")
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"@type":"/poa.MsgAddValidator"`, `"@type":"/cosmos.crypto.ed25519.PubKey"`, `"key":"VX8="`, `9007199254740993`, `18446744073709551615`, `"deposit":"10stake"`, `ipfs://CID`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	for _, unwanted := range []string{`"id"`, `"total_deposit"`, `"status"`, `"tally"`} {
		if strings.Contains(string(b), unwanted) {
			t.Errorf("record field in draft %s", b)
		}
	}
	if !strings.Contains(string(p.MessagePayloads[0]), `"type":"/poa.MsgAddValidator"`) {
		t.Fatal("record was modified")
	}
	parsed, err := ParseProposalDocument(b)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(parsed)
	if !strings.Contains(string(again), "9007199254740993") {
		t.Fatal(string(again))
	}
}
