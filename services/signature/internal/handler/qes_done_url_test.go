package handler

import (
	"net/url"
	"testing"
)

// A QTSP error string used to be appended to the /sign/done redirect raw, so
// "&" / "#" in it injected or overrode query parameters. Every value must
// round-trip intact and nothing else may appear.
func TestQESDoneURL_EncodesValues(t *testing.T) {
	reason := "tsp: HTTP 400: bad code&session=evil#frag"
	got := qesDoneURL("/sign/done", "6f1c0b9e-1d2a-4c55-9a0e-2b8f2f1f0c11", "failed", reason)

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	if u.Path != "/sign/done" || u.Fragment != "" {
		t.Fatalf("got %q: want path /sign/done and no fragment", got)
	}
	q := u.Query()
	if len(q) != 3 {
		t.Fatalf("got params %v, want exactly session/status/reason", q)
	}
	if q.Get("session") != "6f1c0b9e-1d2a-4c55-9a0e-2b8f2f1f0c11" || q.Get("status") != "failed" || q.Get("reason") != reason {
		t.Fatalf("values did not round-trip: %v", q)
	}

	if got := qesDoneURL("/sign/done", "s", "completed", ""); got != "/sign/done?session=s&status=completed" {
		t.Fatalf("no-reason URL = %q", got)
	}
}
