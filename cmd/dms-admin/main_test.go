package main

import (
	"bytes"
	"strings"
	"testing"
)

// R-8 (2026-10-12 review addendum): `dms-admin rotate-secrets` wrote keys
// nothing reads, printed 64-bit key prefixes and reported "Rotation
// complete". The command is gone; tenant KEK rotation is `kms rotate`.
func TestDmsAdmin_NoRotateSecretsCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"rotate-secrets"}, &out, &errOut)
	if code == 0 {
		t.Fatalf("rotate-secrets must not be a command; exit=0 stdout=%q", out.String())
	}
	if !strings.Contains(errOut.String(), "unknown command: rotate-secrets") {
		t.Fatalf("expected unknown-command error, got stderr=%q", errOut.String())
	}
	if strings.Contains(out.String(), "Rotation complete") {
		t.Fatalf("rotate-secrets still ran: %q", out.String())
	}

	out.Reset()
	errOut.Reset()
	if code := run(nil, &out, &errOut); code == 0 {
		t.Fatal("no command must exit non-zero")
	}
	usage := out.String() + errOut.String()
	if strings.Contains(usage, "rotate-secrets") {
		t.Fatalf("usage still advertises rotate-secrets: %q", usage)
	}
	if !strings.Contains(usage, "dms-admin kms rotate") {
		t.Fatalf("usage must point KEK rotation at `dms-admin kms rotate`: %q", usage)
	}
}
