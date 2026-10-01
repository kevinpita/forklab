package progress

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }
func TestOptionalReporterAndTransport(t *testing.T) {
	var absent Reporter
	absent.Emit("binary.download", "Downloading", Started)
	absent.Bytes("binary.download", "Downloading", 1, -1)
	var out bytes.Buffer
	r := JSON(&out)
	r.Emit("binary.download", "Downloading", Started)
	r.Bytes("binary.download", "Downloading", 10, 10)
	r.Emit("binary.download", "Downloaded", Completed)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("events: %s", out.String())
	}
	e, ok := Decode([]byte(lines[1]))
	if !ok || e.Total == nil || *e.Total != 10 || e.Done != 10 {
		t.Fatalf("counts lost: %+v", e)
	}
	JSON(failingWriter{}).Emit("lab.publish", "Created", Completed)
}

func TestUnknownTotalsAndMalformedTransport(t *testing.T) {
	var e Event
	Reporter(func(event Event) { e = event }).Bytes("download", "Downloading", 55, -1)
	if e.Total != nil || e.Done != 55 {
		t.Fatalf("unknown total: %+v", e)
	}
	for _, line := range []string{`{"type":"other","version":1,"event":{}}`, `{"type":"forklab.progress","version":2,"event":{}}`, `{"type":"forklab.progress","version":1,"event":{"phase":"x","message":"x","state":"progress","done":-1}}`, `{"type":"forklab.progress","version":1,"event":{"phase":"x","message":"x","state":"invented"}}`} {
		if _, ok := Decode([]byte(line)); ok {
			t.Errorf("accepted %s", line)
		}
	}
}

func TestLongUnicodeDetailsRemainDecodable(t *testing.T) {
	var out bytes.Buffer
	JSON(&out)(Event{Phase: "download", Message: strings.Repeat("界", 100), Detail: strings.Repeat("界", 1000), State: Started})
	e, ok := Decode(bytes.TrimSpace(out.Bytes()))
	if !ok || !utf8.ValidString(e.Message) || !utf8.ValidString(e.Detail) {
		t.Fatalf("invalid Unicode event: %s", out.String())
	}
}
