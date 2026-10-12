//go:build integration

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/auth"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
)

// proxyStorageStub fakes the storage gRPC client behind StorageProxy. It
// records what the proxy forwards and answers with canned responses; RPCs a
// test does not configure fall through to the nil embedded interface.
type proxyStorageStub struct {
	sedocv1.StorageServiceClient

	mu        sync.Mutex
	initiated []*sedocv1.InitiateUploadRequest
	downloads []*sedocv1.GetDownloadURLRequest
	complete  *sedocv1.CompleteUploadResponse
}

func (s *proxyStorageStub) InitiateUpload(_ context.Context, in *sedocv1.InitiateUploadRequest, _ ...grpc.CallOption) (*sedocv1.InitiateUploadResponse, error) {
	s.mu.Lock()
	s.initiated = append(s.initiated, in)
	s.mu.Unlock()
	return &sedocv1.InitiateUploadResponse{UploadId: "u", PresignedPutUrl: "http://s3.invalid/put"}, nil
}

func (s *proxyStorageStub) CompleteUpload(context.Context, *sedocv1.CompleteUploadRequest, ...grpc.CallOption) (*sedocv1.CompleteUploadResponse, error) {
	return s.complete, nil
}

func (s *proxyStorageStub) GetDownloadURL(_ context.Context, in *sedocv1.GetDownloadURLRequest, _ ...grpc.CallOption) (*sedocv1.GetDownloadURLResponse, error) {
	s.mu.Lock()
	s.downloads = append(s.downloads, in)
	s.mu.Unlock()
	return &sedocv1.GetDownloadURLResponse{Url: "http://s3.invalid/get"}, nil
}

// serveProxy runs one request through the proxy routes as the given caller
// (the identity SessionAuth would have put on the context).
func serveProxy(t *testing.T, p *StorageProxy, ctx context.Context, user auth.UserInfo, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	p.Register(mux)
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req = req.WithContext(auth.WithUser(auth.SetTenantID(ctx, user.TenantID), user))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}
