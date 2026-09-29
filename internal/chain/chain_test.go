package chain

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStatus(t *testing.T) {
	tests := []struct {
		chain string
		want  Status
	}{
		{"simd", Status{
			NodeID: "a5fc45d2487b24cf6db3902d1d5a8c89030345f6", Moniker: "fork0", Network: "mainnet-1",
			LatestHeight: 100, LatestBlockTime: time.Date(2026, 9, 29, 23, 4, 40, 32397946, time.UTC),
			EarliestHeight: 83, ValidatorAddress: "0A8C9CFF520EB1E9102EF239D8B6EEC91EC23141", VotingPower: 135,
		}},
		{"exrpd", Status{
			NodeID: "65fb934a9c5fe3ab21070c9a1519687e12027330", Moniker: "node0", Network: "xrplevm_1449999-1",
			LatestHeight: 32, LatestBlockTime: time.Date(2026, 9, 29, 23, 6, 13, 354804219, time.UTC),
			EarliestHeight: 27, ValidatorAddress: "D210B47681CC1A2C2F2022E2B17A90ADC771AD66", VotingPower: 1000,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.chain, func(t *testing.T) {
			_, c := newFakeNode(t, tt.chain)
			got, err := c.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !got.LatestBlockTime.Equal(tt.want.LatestBlockTime) {
				t.Errorf("block time = %v, want %v", got.LatestBlockTime, tt.want.LatestBlockTime)
			}
			got.LatestBlockTime = tt.want.LatestBlockTime
			if got != tt.want {
				t.Errorf("status = %+v\nwant     %+v", got, tt.want)
			}
		})
	}
}

func TestNetInfo(t *testing.T) {
	tests := []struct {
		chain string
		want  NetInfo
	}{
		{"simd", NetInfo{Listening: true, Peers: []Peer{{ID: "2bcc1099e88183c46268f5cd70040909feeedcd7", Moniker: "fork1", RemoteIP: "127.0.0.1"}}}},
		{"exrpd", NetInfo{Listening: true, Peers: []Peer{{ID: "38cf49c1361f26adeaf042a5bd0bc3cb5db45f9d", Moniker: "node1", RemoteIP: "127.0.0.1"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.chain, func(t *testing.T) {
			_, c := newFakeNode(t, tt.chain)
			got, err := c.NetInfo(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("net info = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestValidatorsWalksEveryPageAtOneHeight(t *testing.T) {
	f, c := newFakeNode(t, "exrpd")
	got, err := c.Validators(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		addr            string
		power, priority int64
	}{
		{"D210B47681CC1A2C2F2022E2B17A90ADC771AD66", 1000, -520},
		{"A13EC63E611A08BECF16EF82385977E85790ED56", 800, -40},
		{"26AAD91C928CE910BC2E5757350720BC085FE4EE", 50, 350},
		{"7AB461DDC567998359697043B8DD19012186CBEE", 30, 210},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d validators, want %d", len(got), len(want))
	}
	for i, w := range want {
		v := got[i]
		if v.Address != w.addr || v.VotingPower != w.power || v.ProposerPriority != w.priority {
			t.Errorf("validator %d = %+v, want %+v", i, v, w)
		}
	}
	if got[0].PubKey != (PubKey{Type: "tendermint/PubKeyEd25519", Value: "GkYzzfPfuPdNJV2KRczoDLTXwVv9Y01mDpRiNJ/djqA="}) {
		t.Errorf("pubkey = %+v", got[0].PubKey)
	}
	wantCalls := []string{
		"/validators?page=1&per_page=100",
		"/validators?height=33&page=2&per_page=100",
		"/validators?height=33&page=3&per_page=100",
		"/validators?height=33&page=4&per_page=100",
	}
	if !reflect.DeepEqual(f.calls, wantCalls) {
		t.Errorf("calls = %q, want %q", f.calls, wantCalls)
	}
}

func TestValidatorsAtHeight(t *testing.T) {
	f, c := newFakeNode(t, "simd")
	if _, err := c.Validators(context.Background(), 90); err != nil {
		t.Fatal(err)
	}
	if f.calls[0] != "/validators?height=90&page=1&per_page=100" {
		t.Errorf("first call = %q", f.calls[0])
	}
}

// voteSummary renders a vote set as "kind[:hash]" per slot for compact
// expectations.
func voteSummary(vs VoteSet) []string {
	out := make([]string, len(vs.Votes))
	for i, v := range vs.Votes {
		out[i] = v.Kind.String()
		if v.Kind == VoteBlock {
			out[i] += ":" + v.BlockHashPrefix
		}
	}
	return out
}

func TestConsensusState(t *testing.T) {
	type tally struct {
		bits         string
		power, total int64
	}
	tests := []struct {
		name, chain, fixture string
		height               int64
		round                int32
		step                 Step
		proposer             string
		prevotes, precommits []string
		prevoteTally         tally
		lastCommit           []string
		lastCommitTally      tally
	}{
		{
			name: "propose step has no votes yet but a last commit", chain: "simd", fixture: "dump_consensus_state_propose.json",
			height: 101, round: 0, step: StepNewHeight, proposer: "0A8C9CFF520EB1E9102EF239D8B6EEC91EC23141",
			prevotes: []string{"missing", "missing", "missing", "missing"}, precommits: []string{"missing", "missing", "missing", "missing"},
			prevoteTally: tally{"____", 0, 300},
			lastCommit:   []string{"block:D924621D5270", "block:D924621D5270", "missing", "missing"}, lastCommitTally: tally{"xx__", 270, 300},
		},
		{
			name: "precommit step with a prevote majority", chain: "simd", fixture: "dump_consensus_state_precommit.json",
			height: 119, round: 0, step: StepPrecommit, proposer: "0A8C9CFF520EB1E9102EF239D8B6EEC91EC23141",
			prevotes: []string{"block:72743D7B862E", "block:72743D7B862E", "missing", "missing"}, precommits: []string{"missing", "missing", "missing", "missing"},
			prevoteTally: tally{"xx__", 270, 300},
			lastCommit:   []string{"block:695D9265A7B1", "block:695D9265A7B1", "missing", "missing"}, lastCommitTally: tally{"xx__", 270, 300},
		},
		{
			name: "halted chain in round 1 reads round 1, not round 0 or 2", chain: "simd", fixture: "dump_consensus_state_halted.json",
			height: 125, round: 1, step: StepPrevote, proposer: "0A8C9CFF520EB1E9102EF239D8B6EEC91EC23141",
			prevotes: []string{"block:78085ABFAC12", "missing", "missing", "missing"}, precommits: []string{"missing", "missing", "missing", "missing"},
			prevoteTally: tally{"x___", 135, 300},
			lastCommit:   []string{"block:E123C0344ED1", "block:E123C0344ED1", "missing", "missing"}, lastCommitTally: tally{"xx__", 270, 300},
		},
		{
			name: "exrpd prevote step", chain: "exrpd", fixture: "dump_consensus_state_prevote.json",
			height: 33, round: 0, step: StepPrevote, proposer: "D210B47681CC1A2C2F2022E2B17A90ADC771AD66",
			prevotes: []string{"block:A4CA9434B045", "missing", "missing", "missing"}, precommits: []string{"missing", "missing", "missing", "missing"},
			prevoteTally: tally{"x___", 1000, 1880},
			lastCommit:   []string{"block:157A0893197B", "block:157A0893197B", "missing", "missing"}, lastCommitTally: tally{"xx__", 1800, 1880},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, c := newFakeNode(t, tt.chain)
			f.dump = tt.fixture
			cs, err := c.ConsensusState(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if cs.Height != tt.height || cs.Round != tt.round || cs.Step != tt.step || cs.ProposerAddress != tt.proposer {
				t.Errorf("h/r/s/proposer = %d/%d/%v/%s, want %d/%d/%v/%s", cs.Height, cs.Round, cs.Step, cs.ProposerAddress, tt.height, tt.round, tt.step, tt.proposer)
			}
			if len(cs.Validators) != 4 {
				t.Errorf("validators = %d, want 4", len(cs.Validators))
			}
			check := func(what string, vs VoteSet, want []string, wantTally *tally) {
				if got := voteSummary(vs); !reflect.DeepEqual(got, want) {
					t.Errorf("%s = %q, want %q", what, got, want)
				}
				if wantTally != nil {
					if got := (tally{vs.Bits, vs.Power, vs.TotalPower}); got != *wantTally {
						t.Errorf("%s tally = %+v, want %+v", what, got, *wantTally)
					}
				}
			}
			check("prevotes", cs.Prevotes, tt.prevotes, &tt.prevoteTally)
			check("precommits", cs.Precommits, tt.precommits, nil)
			check("last commit", cs.LastCommit, tt.lastCommit, &tt.lastCommitTally)
		})
	}
}

func TestParseVote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Vote
	}{
		{
			name: "0.38 precommit for a block",
			in:   "Vote{1:AC079252AA31 100/00/SIGNED_MSG_TYPE_PRECOMMIT(Precommit) D924621D5270 4A261573E0F5 000000000000 @ 2026-09-29T23:04:40.939067891Z}",
			want: Vote{Kind: VoteBlock, ValidatorIndex: 1, ValidatorAddressPrefix: "AC079252AA31", Height: 100, Round: 0, Type: Precommit, BlockHashPrefix: "D924621D5270"},
		},
		{
			name: "0.38 prevote for nil",
			in:   "Vote{0:0A8C9CFF520E 125/00/SIGNED_MSG_TYPE_PREVOTE(Prevote) 000000000000 F3DAD93A0E3A 000000000000 @ 2026-09-29T23:05:12.288430641Z}",
			want: Vote{Kind: VoteNil, ValidatorIndex: 0, ValidatorAddressPrefix: "0A8C9CFF520E", Height: 125, Round: 0, Type: Prevote},
		},
		{
			name: "0.37 layout without the extension fingerprint",
			in:   "Vote{2:BFCB14304EFC 7/03/SIGNED_MSG_TYPE_PREVOTE(Prevote) 72743D7B862E 28DBDB18B32C @ 2023-05-01T10:00:00.000000001Z}",
			want: Vote{Kind: VoteBlock, ValidatorIndex: 2, ValidatorAddressPrefix: "BFCB14304EFC", Height: 7, Round: 3, Type: Prevote, BlockHashPrefix: "72743D7B862E"},
		},
		{
			name: "missing",
			in:   "nil-Vote",
			want: Vote{Kind: VoteMissing, ValidatorIndex: 3},
		},
		{
			name: "truncated string from the prototype sample",
			in:   "Vote{0:0A8C9CFF520E 127/00/SIGNED_MSG_TYPE_PR",
			want: Vote{Kind: VoteUnknown, ValidatorIndex: 3, Raw: "Vote{0:0A8C9CFF520E 127/00/SIGNED_MSG_TYPE_PR"},
		},
		{name: "empty", in: "", want: Vote{Kind: VoteUnknown, ValidatorIndex: 3}},
		{name: "not a vote", in: "garbage", want: Vote{Kind: VoteUnknown, ValidatorIndex: 3, Raw: "garbage"}},
		{name: "empty braces", in: "Vote{}", want: Vote{Kind: VoteUnknown, ValidatorIndex: 3, Raw: "Vote{}"}},
		{
			name: "bad index",
			in:   "Vote{x:AC079252AA31 100/00/SIGNED_MSG_TYPE_PRECOMMIT(Precommit) D924621D5270 4A261573E0F5 @ t}",
			want: Vote{Kind: VoteUnknown, ValidatorIndex: 3, Raw: "Vote{x:AC079252AA31 100/00/SIGNED_MSG_TYPE_PRECOMMIT(Precommit) D924621D5270 4A261573E0F5 @ t}"},
		},
		{
			name: "unknown message type",
			in:   "Vote{1:AC079252AA31 100/00/SIGNED_MSG_TYPE_PROPOSAL(Proposal) D924621D5270 4A261573E0F5 @ t}",
			want: Vote{Kind: VoteUnknown, ValidatorIndex: 3, Raw: "Vote{1:AC079252AA31 100/00/SIGNED_MSG_TYPE_PROPOSAL(Proposal) D924621D5270 4A261573E0F5 @ t}"},
		},
		{
			name: "height/round/type missing a part",
			in:   "Vote{1:AC079252AA31 100/SIGNED_MSG_TYPE_PREVOTE(Prevote) D924621D5270 4A261573E0F5 @ t}",
			want: Vote{Kind: VoteUnknown, ValidatorIndex: 3, Raw: "Vote{1:AC079252AA31 100/SIGNED_MSG_TYPE_PREVOTE(Prevote) D924621D5270 4A261573E0F5 @ t}"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseVote(3, tt.in); got != tt.want {
				t.Errorf("parseVote(%q)\n got %+v\nwant %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseBitArray(t *testing.T) {
	tests := []struct {
		in           string
		bits         string
		power, total int64
		fraction     float64
	}{
		{"BA{4:xx__} 270/300 = 0.90", "xx__", 270, 300, 0.9},
		{"BA{4:x___} 1000/1880 = 0.53", "x___", 1000, 1880, 1000.0 / 1880},
		{"BA{4:____} 0/300 = 0.00", "____", 0, 300, 0},
		{"BA{4:xx_} 270/300 = 0.90", "", 0, 0, 0},
		{"BA{4:xx__ 270/300", "", 0, 0, 0},
		{"nil-BitArray", "", 0, 0, 0},
		{"", "", 0, 0, 0},
	}
	for _, tt := range tests {
		vs := parseVoteSet(nil, tt.in)
		if vs.Bits != tt.bits || vs.Power != tt.power || vs.TotalPower != tt.total || vs.Fraction() != tt.fraction {
			t.Errorf("%q = %q %d/%d %v, want %q %d/%d %v", tt.in, vs.Bits, vs.Power, vs.TotalPower, vs.Fraction(), tt.bits, tt.power, tt.total, tt.fraction)
		}
	}
}

func TestAvgBlockTime(t *testing.T) {
	tests := []struct {
		name string
		n    int64
		want time.Duration
	}{
		// (t32 - t29) / 3 from the exrpd capture.
		{"last 3 blocks", 3, 4220416594},
		// Clamped to (t32 - t28) / 4. Block 27 is the fork's initial height and
		// carries the genesis time, 55 minutes before block 28.
		{"more blocks than the node holds skips the initial block", 100, 4393672276},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, c := newFakeNode(t, "exrpd")
			got, err := c.AvgBlockTime(context.Background(), tt.n)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("avg = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAvgBlockTimeNeedsTwoBlocks(t *testing.T) {
	status := strings.Replace(readFixture(t, "exrpd/status.json"), `"latest_block_height": "32"`, `"latest_block_height": "28"`, 1)
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(status)) })
	if _, err := c.AvgBlockTime(context.Background(), 10); !errors.Is(err, ErrNotEnoughBlocks) {
		t.Errorf("err = %v, want ErrNotEnoughBlocks", err)
	}
}

func TestRPCError(t *testing.T) {
	body := readFixture(t, "simd/error_page_range.json")
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(body))
	})
	_, err := c.Validators(context.Background(), 0)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v, want *RPCError", err)
	}
	want := RPCError{Method: "validators", Code: -32603, Message: "Internal error", Data: "page should be within [1, 1] range, given 9"}
	if *rpcErr != want {
		t.Errorf("rpc error = %+v, want %+v", *rpcErr, want)
	}
}

func TestHTTPErrorWithoutJSON(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	})
	_, err := c.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "502 Bad Gateway") {
		t.Errorf("err = %v, want 502 status", err)
	}
}

func TestRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = c.Status(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("returned after %v, want about 50ms", d)
	}
}

func TestCallerCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	if _, err := c.NetInfo(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want canceled", err)
	}
}

func TestWaitHeight(t *testing.T) {
	status := readFixture(t, "simd/status.json")
	var polls atomic.Int32
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		n := polls.Add(1)
		if n <= 2 {
			http.Error(w, "node restarting", http.StatusServiceUnavailable)
			return
		}
		// Height climbs 98, 99, 100, ... from the third poll on.
		h := `"latest_block_height": "` + strconv.Itoa(int(95+n)) + `"`
		_, _ = w.Write([]byte(strings.Replace(status, `"latest_block_height": "100"`, h, 1)))
	})
	c.pollInterval = time.Millisecond
	got, err := c.WaitHeight(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if got != 100 || polls.Load() != 5 {
		t.Errorf("reached %d after %d polls, want 100 after 5", got, polls.Load())
	}
}

func TestWaitHeightGivesUpWithLastError(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "node restarting", http.StatusServiceUnavailable)
	})
	c.pollInterval = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.WaitHeight(ctx, 10)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "503 Service Unavailable") {
		t.Errorf("err = %v, want deadline exceeded naming the 503", err)
	}
}

func TestNew(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"http://127.0.0.1:26657", "http://127.0.0.1:26657"},
		{"tcp://127.0.0.1:26657", "http://127.0.0.1:26657"},
		{"https://rpc.example.com/", "https://rpc.example.com/"},
		{"127.0.0.1:26657", ""},
		{"", ""},
		{"ftp://host:1", ""},
	}
	for _, tt := range tests {
		c, err := New(tt.in, 0)
		switch {
		case tt.want == "" && err == nil:
			t.Errorf("New(%q) accepted, want error", tt.in)
		case tt.want != "" && err != nil:
			t.Errorf("New(%q): %v", tt.in, err)
		case tt.want != "" && (c.base.String() != tt.want || c.timeout != DefaultTimeout):
			t.Errorf("New(%q) = %s timeout %v, want %s timeout %v", tt.in, c.base, c.timeout, tt.want, DefaultTimeout)
		}
	}
}

func serve(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
