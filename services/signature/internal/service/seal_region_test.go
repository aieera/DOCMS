// Region-pin (data-residency) enforcement on the server-seal paths.
//
// Regression: SealVersion/SealCeremony wrote the sealed blob with NO
// RegionPin, so ingest defaulted it to us-east-1 — forcing every sealed
// artifact into that region and breaching residency for any non-us-east
// document. These tests pin: a doc whose region_pin is eu-central-1
// seals into eu-central-1 (the pin reaches the storage client), and an
// unresolvable/empty pin REJECTS the seal (fail closed, no write to a
// default region).
package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
)

// sealTestService builds a Service+Sealer wired to a fake signer + fake
// ingest, with fetchVersionPDF served by an httptest doc-service stub
// and the region resolver injected (regionFn) so no DB is needed.
func sealTestService(t *testing.T, fake *fakeIngestClient, regionFn func(ctx context.Context, tenantID, documentID string) (string, error)) *Service {
	t.Helper()
	docSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("%PDF-1.7 source bytes"))
	}))
	t.Cleanup(docSrv.Close)

	svc := &Service{log: zerolog.Nop()}
	svc.sealer = &Sealer{
		signer:      &fakeSigner{},
		ingest:      &ingestPipeline{client: fake, regionFn: regionFn},
		docHTTPBase: docSrv.URL,
		httpClient:  docSrv.Client(),
	}
	return svc
}

func TestSealVersion_PinReachesStorageClient(t *testing.T) {
	fake := &fakeIngestClient{}
	svc := sealTestService(t, fake, func(context.Context, string, string) (string, error) {
		return "eu-central-1", nil // the document's pin
	})

	res, err := svc.SealVersion(context.Background(),
		"11111111-1111-1111-1111-111111111111", "doc-1", "ver-1", "user-1",
		"SeDoc Organizational Seal", "test seal")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if res.NewVersionID == "" {
		t.Fatal("expected a new version id")
	}
	if fake.putCalls != 1 {
		t.Fatalf("put_calls = %d; want 1", fake.putCalls)
	}
	if fake.lastPut.RegionPin != "eu-central-1" {
		t.Fatalf("sealed blob region = %q; want eu-central-1 (residency pin must reach storage)", fake.lastPut.RegionPin)
	}
}

func TestSealCeremony_PinReachesStorageClient(t *testing.T) {
	fake := &fakeIngestClient{}
	svc := sealTestService(t, fake, func(context.Context, string, string) (string, error) {
		return "ap-southeast-2", nil
	})

	_, err := svc.SealCeremony(context.Background(),
		"11111111-1111-1111-1111-111111111111", "doc-1", "ver-1", "user-1",
		[]CeremonySigner{{Name: "Alice", Email: "alice@acme.com"}})
	if err != nil {
		t.Fatalf("ceremony: %v", err)
	}
	if fake.lastPut.RegionPin != "ap-southeast-2" {
		t.Fatalf("ceremony blob region = %q; want ap-southeast-2", fake.lastPut.RegionPin)
	}
}

func TestSealVersion_UnpinnableRejected(t *testing.T) {
	// region resolves EMPTY (no region_pin) → seal must be rejected and
	// nothing written (no default-region fallback).
	fake := &fakeIngestClient{}
	svc := sealTestService(t, fake, func(context.Context, string, string) (string, error) {
		return "", nil
	})
	_, err := svc.SealVersion(context.Background(),
		"11111111-1111-1111-1111-111111111111", "doc-1", "ver-1", "user-1", "", "")
	if err == nil {
		t.Fatal("expected seal to be rejected when the region pin can't be honored")
	}
	if fake.putCalls != 0 {
		t.Fatalf("put_calls = %d; a rejected seal must write NOTHING", fake.putCalls)
	}
}

func TestSealVersion_RegionResolveErrorRejected(t *testing.T) {
	// region lookup ERRORS (e.g. RLS hides the doc / DB down) → fail
	// closed, never a default-region write.
	fake := &fakeIngestClient{}
	svc := sealTestService(t, fake, func(context.Context, string, string) (string, error) {
		return "", errors.New("rls: 0 rows")
	})
	_, err := svc.SealVersion(context.Background(),
		"11111111-1111-1111-1111-111111111111", "doc-1", "ver-1", "user-1", "", "")
	if err == nil {
		t.Fatal("expected seal rejection on region resolve error")
	}
	if fake.putCalls != 0 {
		t.Fatalf("put_calls = %d; want 0", fake.putCalls)
	}
}

func TestPutAndCreateVersion_FailsClosedOnEmptyRegion(t *testing.T) {
	// The choke point: an unset region is a hard error — no silent
	// us-east-1 default — so the storage client is never called.
	fake := &fakeIngestClient{}
	p := &ingestPipeline{client: fake}
	_, _, err := p.PutAndCreateVersion(context.Background(),
		PutSignedBlobInput{TenantID: "t-1", Bytes: []byte("%PDF")},
		"doc-1", "u-1", "seal")
	if err == nil {
		t.Fatal("expected region_pin-required error")
	}
	if fake.putCalls != 0 {
		t.Fatalf("put_calls = %d; the fail-closed guard must precede any storage write", fake.putCalls)
	}
}

func TestResolveRegionOrFail(t *testing.T) {
	pinned := &ingestPipeline{regionFn: func(context.Context, string, string) (string, error) {
		return "eu-west-1", nil
	}}
	if r, err := pinned.ResolveRegionOrFail(context.Background(), "t", "d"); err != nil || r != "eu-west-1" {
		t.Fatalf("pinned: got (%q, %v); want (eu-west-1, nil)", r, err)
	}

	empty := &ingestPipeline{regionFn: func(context.Context, string, string) (string, error) {
		return "", nil
	}}
	if _, err := empty.ResolveRegionOrFail(context.Background(), "t", "d"); err == nil {
		t.Fatal("empty region_pin must be an error")
	}

	broken := &ingestPipeline{regionFn: func(context.Context, string, string) (string, error) {
		return "", errors.New("db down")
	}}
	if _, err := broken.ResolveRegionOrFail(context.Background(), "t", "d"); err == nil {
		t.Fatal("resolve error must propagate as a rejection")
	}
}
