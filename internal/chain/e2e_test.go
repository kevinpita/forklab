//go:build e2e

package chain

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveNode runs every call against the node at $FORKLAB_RPC.
func TestLiveNode(t *testing.T) {
	rpc := os.Getenv("FORKLAB_RPC")
	if rpc == "" {
		t.Skip("FORKLAB_RPC not set")
	}
	c, err := New(rpc, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("status: %+v", st)
	if st.LatestHeight <= 0 || st.Network == "" || st.NodeID == "" {
		t.Errorf("status looks empty: %+v", st)
	}

	vals, err := c.Validators(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vals {
		t.Logf("validator %s power=%d priority=%d", v.Address, v.VotingPower, v.ProposerPriority)
	}
	if len(vals) == 0 {
		t.Error("empty validator set")
	}

	cs, err := c.ConsensusState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("consensus: h=%d r=%d step=%v proposer=%s", cs.Height, cs.Round, cs.Step, cs.ProposerAddress)
	for _, set := range []struct {
		name string
		vs   VoteSet
	}{{"prevotes", cs.Prevotes}, {"precommits", cs.Precommits}, {"last_commit", cs.LastCommit}} {
		t.Logf("%s %s %d/%d = %.2f", set.name, set.vs.Bits, set.vs.Power, set.vs.TotalPower, set.vs.Fraction())
		for _, v := range set.vs.Votes {
			t.Logf("  [%d] %v %v %s", v.ValidatorIndex, v.Kind, v.Type, v.BlockHashPrefix)
			if v.Kind == VoteUnknown {
				t.Errorf("unparsed vote %q", v.Raw)
			}
		}
	}
	if len(cs.LastCommit.Votes) != len(cs.Validators) {
		t.Errorf("last commit has %d slots for %d validators", len(cs.LastCommit.Votes), len(cs.Validators))
	}

	ni, err := c.NetInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("net_info: listening=%v peers=%+v", ni.Listening, ni.Peers)

	avg, err := c.AvgBlockTime(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("avg block time over 10: %v", avg)

	target := st.LatestHeight + 2
	got, err := c.WaitHeight(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("reached height %d (waited for %d)", got, target)
}
