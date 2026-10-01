package progress

import (
	"encoding/json"
	"io"
	"sync"
	"time"
	"unicode/utf8"
)

type State string

const (
	Started   State = "started"
	Updated   State = "progress"
	Completed State = "completed"
)

type Event struct {
	Phase   string `json:"phase"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	State   State  `json:"state"`
	Done    int64  `json:"done,omitempty"`
	Total   *int64 `json:"total,omitempty"`
	Unit    string `json:"unit,omitempty"`
}

type Reporter func(Event)

func (r Reporter) Emit(phase, message string, state State) {
	if r != nil {
		r(Event{Phase: phase, Message: message, State: state})
	}
}

func (r Reporter) Bytes(phase, message string, done, total int64) {
	if r == nil {
		return
	}
	e := Event{Phase: phase, Message: message, State: Updated, Done: done, Unit: "bytes"}
	if total > 0 {
		e.Total = &total
	}
	r(e)
}

type envelope struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	Event   Event  `json:"event"`
}

func JSON(w io.Writer) Reporter {
	var mu sync.Mutex
	var last time.Time
	var lastPhase string
	var lastState State
	return func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		if e.State == Updated && lastState == Updated && lastPhase == e.Phase && time.Since(last) < 100*time.Millisecond && (e.Total == nil || e.Done < *e.Total) {
			return
		}
		last = time.Now()
		lastPhase, lastState = e.Phase, e.State
		e.Message = truncate(e.Message, 256)
		e.Detail = truncate(e.Detail, 2048)
		_ = json.NewEncoder(w).Encode(envelope{Type: "forklab.progress", Version: 1, Event: e})
	}
}

func Decode(line []byte) (Event, bool) {
	var env envelope
	if json.Unmarshal(line, &env) != nil || env.Type != "forklab.progress" || env.Version != 1 {
		return Event{}, false
	}
	e := env.Event
	if e.Phase == "" || len(e.Phase) > 128 || e.Message == "" || len(e.Message) > 256 || len(e.Detail) > 2048 || e.Done < 0 || (e.Total != nil && *e.Total <= 0) {
		return Event{}, false
	}
	if e.State != Started && e.State != Updated && e.State != Completed {
		return Event{}, false
	}
	if e.Unit != "" && e.Unit != "bytes" && e.Unit != "nodes" {
		return Event{}, false
	}
	return e, true
}

func truncate(s string, n int) string {
	if len(s) > n {
		for !utf8.RuneStart(s[n]) {
			n--
		}
		return s[:n]
	}
	return s
}
