package pades

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// parseWithin runs parsePDF and fails the test if it does not return within d
// (the pre-fix parser is quadratic in the number of /Type /Sig hits).
func parseWithin(t *testing.T, raw []byte, d time.Duration) (*parsedDoc, error) {
	t.Helper()
	type result struct {
		doc *parsedDoc
		err error
	}
	done := make(chan result, 1)
	go func() {
		doc, err := parsePDF(raw)
		done <- result{doc, err}
	}()
	select {
	case r := <-done:
		return r.doc, r.err
	case <-time.After(d):
		t.Fatalf("parsePDF did not finish within %s on %d bytes of hostile input", d, len(raw))
		return nil, nil
	}
}

func TestParser_HostileRepetitionBounded(t *testing.T) {
	// 200k "/Type /Sig" tokens inside one never-closed dict: every hit used
	// to walk back to the opener and forward to EOF.
	hostile := []byte("%PDF-1.7\n<<" + strings.Repeat("/Type /Sig ", 200_000))
	_, err := parseWithin(t, hostile, 5*time.Second)
	if !errors.Is(err, ErrParse) {
		t.Fatalf("err = %v, want ErrParse for more than %d signature dictionaries", err, maxSigDicts)
	}

	// A /Sig dict whose opener is more than the walk limit before the
	// /Type /Sig token is not walked to.
	far := []byte("%PDF-1.7\n1 0 obj <<" + strings.Repeat(" ", maxDictWalk+16) +
		"/Type /Sig /ByteRange [0 1 2 3] /Contents <abcd> >> endobj\n%%EOF\n")
	doc, err := parseWithin(t, far, 5*time.Second)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(doc.Signatures) != 0 {
		t.Fatalf("found %d signatures; the dict walk must stop at %d bytes", len(doc.Signatures), maxDictWalk)
	}

	// Exactly the cap is still accepted.
	var b strings.Builder
	b.WriteString("%PDF-1.7\n")
	for i := 0; i < maxSigDicts; i++ {
		b.WriteString("<</Type /Sig /ByteRange [0 1 2 3] /Contents <abcd>>>\n")
	}
	doc, err = parseWithin(t, []byte(b.String()), 5*time.Second)
	if err != nil {
		t.Fatalf("parse at the cap: %v", err)
	}
	if len(doc.Signatures) != maxSigDicts {
		t.Fatalf("signatures = %d, want %d", len(doc.Signatures), maxSigDicts)
	}
}

func TestSignedBytes_OverflowRejected(t *testing.T) {
	raw := make([]byte, 100)
	for name, br := range map[string][4]int64{
		"first range overflows":   {1, math.MaxInt64, 0, 1},
		"second range overflows":  {0, 1, 2, math.MaxInt64},
		"both lengths huge":       {0, math.MaxInt64, 1, math.MaxInt64},
		"start beyond end":        {101, 0, 0, 0},
		"negative start":          {-1, 10, 20, 10},
		"negative length":         {0, -5, 20, 10},
		"ranges overlap":          {0, 60, 50, 50},
		"second range past end":   {0, 10, 20, 81},
		"max start, zero lengths": {math.MaxInt64, 0, math.MaxInt64, 0},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("signedBytes panicked on %v: %v", br, r)
				}
			}()
			if _, err := signedBytes(raw, br); err == nil {
				t.Fatalf("signedBytes accepted hostile ByteRange %v", br)
			}
		})
	}
	got, err := signedBytes(raw, [4]int64{0, 40, 60, 40})
	if err != nil || len(got) != 80 {
		t.Fatalf("well-formed range: len=%d err=%v", len(got), err)
	}
}

func TestValidate_StopsAtDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v := NewVerifier(VerifierOptions{SkipNetworkLookups: true})
	rep, err := v.Validate(ctx, buildSignedPDFFixture(t))
	if err == nil {
		t.Fatal("expected an error once the validation context is done")
	}
	if rep == nil || !contains(rep.Errors, "validation_timeout") {
		t.Fatalf("expected validation_timeout in report errors, got %+v", rep)
	}
	if rep.TamperEvident {
		t.Fatal("an unfinished validation must not report tamper evidence")
	}
}
