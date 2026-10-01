package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/lab"
	taskprogress "github.com/kevinpita/forklab/internal/progress"
	"github.com/kevinpita/forklab/internal/supervisor"
)

func TestUpgradeProgressFormatRejectedBeforeOpeningLab(t *testing.T) {
	code, stdout, _ := run("upgrade", "schedule", "2.0", "--in", "40", "--lab", "/missing-lab", "--progress", "text", "--json")
	if code != 2 || !strings.Contains(stdout, "--progress must be json") {
		t.Fatalf("code %d: %s", code, stdout)
	}
}

func TestUpgradeGovernanceProgressReportsProposalAndDeadline(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "chain")
	script := `#!/bin/sh
case "$1 $2 $3" in
"tx gov submit-proposal"|"tx gov vote") echo '{"txhash":"ABCD","code":0}' ;;
"q gov proposal")
 if [ -f "` + filepath.Join(dir, "queried") + `" ]; then
 echo '{"proposal":{"id":"2","status":"PROPOSAL_STATUS_PASSED"}}'
 else
 touch "` + filepath.Join(dir, "queried") + `"
 echo '{"proposal":{"id":"2","status":"PROPOSAL_STATUS_VOTING_PERIOD","voting_end_time":"2030-01-01T12:00:00Z"}}'
 fi ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tx, err := os.ReadFile("../chain/testdata/simd/cli/rpc_tx_submit_proposal.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(tx) }))
	defer srv.Close()
	rpc, err := chain.New(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	var events []taskprogress.Event
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var submitted uint64
	id, votes, err := submitAndPass(ctx, chain.CLI{Bin: bin}, rpc, lab.Config{}, chain.ProposalFile{}, time.Second, func(id uint64) { submitted = id }, func(e taskprogress.Event) { events = append(events, e) })
	if err != nil || id != 2 || submitted != 2 || len(votes) != 1 {
		t.Fatalf("id %d submitted %d votes %v err %v", id, submitted, votes, err)
	}
	var completed []string
	for _, e := range events {
		if e.State == taskprogress.Completed {
			completed = append(completed, e.Phase)
		}
	}
	if !slices.Equal(completed, []string{"upgrade.submit", "upgrade.confirm", "upgrade.vote", "upgrade.voting"}) {
		t.Fatalf("completed %v", completed)
	}
	if !slices.ContainsFunc(events, func(e taskprogress.Event) bool {
		return strings.Contains(e.Detail, "Proposal #2 · VOTING_PERIOD") && strings.Contains(e.Detail, "voting ends")
	}) {
		t.Fatalf("no voting detail: %+v", events)
	}
}

func TestUpgradeSwapProgressReportsActualNodeStates(t *testing.T) {
	dir := newShellLab(t)
	t.Cleanup(func() { run("supervisor", "down", "--lab", dir, "--json") })
	sup, err := ensureSupervisor(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sup.SetUpgrade(&supervisor.Upgrade{Name: "v2", Height: 200, Version: "2.0", Binary: "/bin/sh", AutoSwap: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := sup.Start(supervisor.All); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var detail string
	_, err = waitSwapped(ctx, dir, "v2", func(e taskprogress.Event) {
		if e.State == taskprogress.Updated {
			detail = e.Detail
		}
	})
	if err != nil || !strings.Contains(detail, "Halt at height 200") || !strings.Contains(detail, "node0 swapped") {
		t.Fatalf("detail %q err %v", detail, err)
	}
}

func TestUpgradeRecoveryProgressCountsNodesAtTargetHeight(t *testing.T) {
	status, err := os.ReadFile("../chain/testdata/simd/status.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(status) }))
	defer srv.Close()
	rpc, err := chain.New(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	e := labEnv{cfg: lab.Config{Nodes: []lab.Node{{Name: "node0"}, {Name: "node1"}}}, clients: []*chain.Client{rpc, rpc}}
	var counts []int64
	var complete bool
	height, err := e.waitPast(t.Context(), 99, func(event taskprogress.Event) {
		if event.State == taskprogress.Updated {
			counts = append(counts, event.Done)
			if event.Total == nil || *event.Total != 2 {
				t.Errorf("bad total: %+v", event)
			}
		}
		if event.State == taskprogress.Completed {
			complete = true
		}
	})
	if err != nil || height < 99 || !complete || !slices.Equal(counts, []int64{1, 2}) {
		t.Fatalf("height %d counts %v completed %t err %v", height, counts, complete, err)
	}
}
