package ingest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
)

// fakeStorage captures the identity metadata of InitiateUpload and then
// fails it, which drives IngestFile into its rollback (a second document
// REST call) without needing MinIO.
type fakeStorage struct {
	sedocv1.StorageServiceClient
	md metadata.MD
}

func (f *fakeStorage) InitiateUpload(ctx context.Context, _ *sedocv1.InitiateUploadRequest, _ ...grpc.CallOption) (*sedocv1.InitiateUploadResponse, error) {
	f.md, _ = metadata.FromOutgoingContext(ctx)
	return nil, errors.New("stop after initiate")
}

type seenRequest struct {
	method, path, role, user, tenant, internalKey string
}

func newDocServer(t *testing.T) (*httptest.Server, *[]seenRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, seenRequest{r.Method, r.URL.Path, r.Header.Get("X-User-Role"), r.Header.Get("X-User-ID"), r.Header.Get("X-Auth-Tenant-ID"), r.Header.Get("X-Internal-Service-Key")})
		mu.Unlock()
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"0b9f7c2e-1111-4a3e-9c1e-123456789abc"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

const (
	ingTenant = "7a1e2b3c-4d5e-4f60-8a1b-2c3d4e5f6a7b"
	ingActor  = "1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f"
)

func TestIngest_UsesCreatorRole(t *testing.T) {
	srv, seen := newDocServer(t)
	st := &fakeStorage{}
	c := &Client{
		docBaseURL:  srv.URL,
		internalKey: "internal-key",
		storage:     st,
		httpc:       &http.Client{Timeout: 5 * time.Second},
		log:         zerolog.Nop(),
		roleOf: func(_ context.Context, tenantID, userID string) (string, error) {
			if tenantID != ingTenant || userID != ingActor {
				t.Fatalf("role lookup for %s/%s", tenantID, userID)
			}
			return "member", nil
		},
	}
	_, err := c.IngestFile(context.Background(), ingTenant, ingActor, "", "ws", "fd", "a.pdf", "application/pdf", []byte("%PDF"), nil)
	if err == nil {
		t.Fatal("expected the fake initiate failure to surface")
	}
	if got := st.md.Get("x-user-role"); len(got) != 1 || got[0] != "member" {
		t.Fatalf("storage metadata x-user-role = %v, want [member]", got)
	}
	if len(*seen) == 0 {
		t.Fatal("no document REST calls made")
	}
	for _, r := range *seen {
		if r.role != "member" {
			t.Errorf("%s %s sent X-User-Role %q, want the creator's real role %q", r.method, r.path, r.role, "member")
		}
		if r.user != ingActor || r.tenant != ingTenant {
			t.Errorf("%s %s identity = %s/%s", r.method, r.path, r.tenant, r.user)
		}
	}
}

// No active membership → no ingest at all (never a fabricated admin).
func TestIngest_NoActiveCreatorRefuses(t *testing.T) {
	srv, seen := newDocServer(t)
	c := &Client{
		docBaseURL: srv.URL, internalKey: "k", storage: &fakeStorage{},
		httpc: &http.Client{Timeout: 5 * time.Second}, log: zerolog.Nop(),
		roleOf: func(context.Context, string, string) (string, error) { return "", ErrNoActiveActor },
	}
	if _, err := c.IngestFile(context.Background(), ingTenant, ingActor, "", "ws", "fd", "a", "text/plain", []byte("x"), nil); !errors.Is(err, ErrNoActiveActor) {
		t.Fatalf("err = %v, want ErrNoActiveActor", err)
	}
	if _, err := c.IngestFile(context.Background(), ingTenant, "", "", "ws", "fd", "a", "text/plain", []byte("x"), nil); !errors.Is(err, ErrNoActiveActor) {
		t.Fatalf("empty actor: err = %v, want ErrNoActiveActor", err)
	}
	if len(*seen) != 0 {
		t.Fatalf("document REST called %d times for an actor with no active membership", len(*seen))
	}
}
