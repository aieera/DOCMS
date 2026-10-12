package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aieera/sedoc/services/signature/internal/signer"
)

// levelSigner behaves like fakeSigner but reports a fixed PAdES level, the
// way the DSS sidecar silently answers PAdES-B-B when it has no TSA.
type levelSigner struct {
	fakeSigner
	level signer.Level
}

func (l *levelSigner) Sign(ctx context.Context, req signer.Request) (*signer.Response, error) {
	resp, err := l.fakeSigner.Sign(ctx, req)
	if err != nil {
		return nil, err
	}
	resp.Level = l.level
	return resp, nil
}

func TestSealer_RejectsDowngradedLevel(t *testing.T) {
	ctx := context.Background()
	signers := []CeremonySigner{{Name: "Alice", Email: "alice@acme.com"}}

	for _, got := range []signer.Level{signer.LevelBB, signer.LevelBT, ""} {
		_, _, _, err := applyCeremonyRevisions(ctx, &levelSigner{level: got}, "t1", "http://tsa.test", []byte("%PDF-1.7"), signers)
		if !errors.Is(err, ErrSealLevelDowngraded) {
			t.Errorf("ceremony: sidecar returned %q for a B-LT request: err = %v, want ErrSealLevelDowngraded", got, err)
		}
	}
	for _, got := range []signer.Level{signer.LevelBLT, signer.LevelBLTA} {
		if _, _, _, err := applyCeremonyRevisions(ctx, &levelSigner{level: got}, "t1", "http://tsa.test", []byte("%PDF-1.7"), signers); err != nil {
			t.Errorf("ceremony: level %q meets the B-LT request: %v", got, err)
		}
	}

	fake := &fakeIngestClient{}
	svc := sealTestService(t, fake, func(context.Context, string, string) (string, error) { return "eu-central-1", nil })
	svc.sealer.signer = &levelSigner{level: signer.LevelBB}
	_, err := svc.SealVersion(ctx, "11111111-1111-1111-1111-111111111111", "doc-1", "ver-1", "user-1", "SeDoc Organizational Seal", "test")
	if !errors.Is(err, ErrSealLevelDowngraded) {
		t.Fatalf("SealVersion: err = %v, want ErrSealLevelDowngraded", err)
	}
	if fake.putCalls != 0 {
		t.Fatalf("a downgraded seal must not be ingested (put_calls = %d)", fake.putCalls)
	}
}

// SEDOC_SEAL_LEVEL is validated once, at startup: a level that needs a
// timestamp authority without SIGNER_TSA_URL must stop the service rather
// than fail every seal later; B-B must be asked for explicitly.
func TestSealLevelConfig_BLTWithoutTSAFailsStartup(t *testing.T) {
	for _, tc := range []struct {
		value, tsa string
		want       signer.Level
		wantErr    bool
	}{
		{"", "", "", true}, // default B-LT, no TSA
		{"B-LT", "", "", true},
		{"B-T", "", "", true},
		{"B-LT", "  ", "", true},
		{"bogus", "http://tsa.test", "", true},
		{"PAdES-B-B", "", "", true},
		{"", "http://tsa.test", signer.LevelBLT, false},
		{"B-LT", "http://tsa.test", signer.LevelBLT, false},
		{"B-T", "http://tsa.test", signer.LevelBT, false},
		{"B-B", "", signer.LevelBB, false},
		{"B-B", "http://tsa.test", signer.LevelBB, false},
	} {
		got, err := ParseSealLevel(tc.value, tc.tsa)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseSealLevel(%q, %q) = %q, want a startup error", tc.value, tc.tsa, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseSealLevel(%q, %q) = %q, %v; want %q", tc.value, tc.tsa, got, err, tc.want)
		}
	}
}

func TestSealer_AcceptsConfiguredBB(t *testing.T) {
	ctx := context.Background()
	fake := &fakeIngestClient{}
	svc := sealTestService(t, fake, func(context.Context, string, string) (string, error) { return "eu-central-1", nil })
	ls := &levelSigner{level: signer.LevelBB}
	svc.sealer.signer = ls
	svc.sealer.level = signer.LevelBB

	res, err := svc.SealVersion(ctx, "11111111-1111-1111-1111-111111111111", "doc-1", "ver-1", "user-1", "SeDoc Organizational Seal", "test")
	if err != nil {
		t.Fatalf("SealVersion with SEDOC_SEAL_LEVEL=B-B: %v", err)
	}
	if res.Level != string(signer.LevelBB) {
		t.Fatalf("Level = %q", res.Level)
	}
	if _, err := svc.SealCeremony(ctx, "11111111-1111-1111-1111-111111111111", "doc-1", "ver-1", "user-1",
		[]CeremonySigner{{Name: "Alice", Email: "alice@acme.com"}}); err != nil {
		t.Fatalf("SealCeremony with SEDOC_SEAL_LEVEL=B-B: %v", err)
	}
	for _, c := range ls.calls {
		if c.Level != signer.LevelBB {
			t.Errorf("the signer was asked for %q, want the configured B-B", c.Level)
		}
	}

	// The configured level is still a floor.
	svc.sealer.level = signer.LevelBT
	if _, err := svc.SealVersion(ctx, "11111111-1111-1111-1111-111111111111", "doc-1", "ver-1", "user-1", "s", "r"); !errors.Is(err, ErrSealLevelDowngraded) {
		t.Fatalf("B-T configured, B-B returned: err = %v, want ErrSealLevelDowngraded", err)
	}
}
