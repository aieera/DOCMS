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
