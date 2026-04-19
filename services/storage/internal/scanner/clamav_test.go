package scanner

import (
	"strings"
	"testing"
)

// parse() is the protocol parser — turns a raw clamd reply into a Result.
// Every branch matters: a mis-parsed response can either admit a virus
// (flagged as OK) or false-quarantine a clean upload.

func TestParse_Clean(t *testing.T) {
	r, err := parse("stream: OK")
	if err != nil {
		t.Fatalf("clean response shouldn't error: %v", err)
	}
	if r.Infected {
		t.Error("clean response marked infected")
	}
	if r.Signature != "" {
		t.Errorf("clean response should have no signature, got %q", r.Signature)
	}
	if r.Raw != "stream: OK" {
		t.Errorf("Raw: got %q", r.Raw)
	}
}

func TestParse_Infected(t *testing.T) {
	r, err := parse("stream: Eicar-Test-Signature FOUND")
	if err != nil {
		t.Fatalf("infected response shouldn't error: %v", err)
	}
	if !r.Infected {
		t.Error("infected response NOT marked infected")
	}
	if r.Signature != "Eicar-Test-Signature" {
		t.Errorf("signature: got %q, want %q", r.Signature, "Eicar-Test-Signature")
	}
}

func TestParse_InfectedWithSpacesInSignature(t *testing.T) {
	// Real ClamAV signatures can contain spaces.
	r, err := parse("stream: Win.Trojan.Foo 1234 FOUND")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !r.Infected {
		t.Fatal("should be infected")
	}
	if r.Signature != "Win.Trojan.Foo 1234" {
		t.Errorf("signature with space mis-parsed: %q", r.Signature)
	}
}

func TestParse_ErrorResponse(t *testing.T) {
	_, err := parse("stream: ERROR: size limit exceeded")
	if err == nil {
		t.Fatal("ERROR response should return an error")
	}
	if !strings.Contains(err.Error(), "clamav error") {
		t.Errorf("error message should mention clamav: %v", err)
	}
}

func TestParse_UnparseableResponse(t *testing.T) {
	// Anything that doesn't match OK/FOUND/ERROR is an internal error.
	// The HTTP layer scrubs the message on the wire, so leaking the raw
	// response in logs is fine.
	_, err := parse("some-unknown-gibberish")
	if err == nil {
		t.Fatal("unrecognized response should error")
	}
}
