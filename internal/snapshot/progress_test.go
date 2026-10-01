package snapshot_test

import (
	"context"
	"slices"
	"testing"

	"github.com/kevinpita/forklab/internal/snapshot"

	"github.com/kevinpita/forklab/internal/progress"
)

func TestExportReportsRealPhasesAndCacheReuse(t *testing.T) {
	in := exportInput(t, fakeBinary(t, fakeChain))
	var events []progress.Event
	in.Reporter = func(e progress.Event) { events = append(events, e) }
	export(t, in)
	for _, phase := range []string{"snapshot.cache", "snapshot.init", "snapshot.extract", "snapshot.export"} {
		i := slices.IndexFunc(events, func(e progress.Event) bool { return e.Phase == phase && e.State == progress.Completed })
		if i < 0 {
			t.Fatalf("phase never completed %s: %+v", phase, events)
		}
	}
	events = nil
	export(t, in)
	if len(events) != 2 || events[1].Message != "Reused cached snapshot export" || events[1].State != progress.Completed {
		t.Fatalf("cache invented work: %+v", events)
	}
}

func TestFailedExportNeverReportsCompletion(t *testing.T) {
	in := exportInput(t, fakeBinary(t, failingChain))
	var events []progress.Event
	in.Reporter = func(e progress.Event) { events = append(events, e) }
	_, err := snapshot.Export(context.Background(), in)
	if err == nil {
		t.Fatal("fixture should fail")
	}
	for _, e := range events {
		if e.Phase == "snapshot.export" && e.State == progress.Completed {
			t.Fatalf("failed export completed: %+v", events)
		}
	}
}
