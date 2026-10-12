package tsp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func selfSignedPEM(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "client"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

// An empty, non-nil RootCAs pool trusts nothing, so every mTLS call to AIS
// failed certificate verification. Nil means "use the system roots".
func TestSwisscomClient_UsesSystemRoots(t *testing.T) {
	certPEM, keyPEM := selfSignedPEM(t)
	c, err := NewSwisscom(SwisscomConfig{
		BaseURL: "https://ais.example", CustomerID: "cust",
		ClientCertPEM: certPEM, ClientKeyPEM: keyPEM,
	})
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil {
		t.Fatal("expected an *http.Transport with a TLS config")
	}
	if tr.TLSClientConfig.RootCAs != nil {
		t.Fatal("RootCAs must be nil (system roots); an empty pool rejects every server certificate")
	}
	if len(tr.TLSClientConfig.Certificates) != 1 {
		t.Fatal("the mTLS client certificate must still be presented")
	}
}

// A 4xx answer is an error before any decoding: a JSON error body that
// happens to share field names must not read as success.
func TestInfoCert_4xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"session_id": "sess", "authorize_url": "https://attacker.example"})
	}))
	defer srv.Close()
	c, err := NewInfoCert(InfoCertConfig{BaseURL: srv.URL, ClientID: "id", ClientSecret: "s", OrgID: "org", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Authorize(context.Background(), AuthorizeReq{SubjectID: "u", DocumentHash: "h", ReturnURL: "https://x"})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
}

func TestIntesi_4xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{"valid": true, "on_trust_list": true})
	}))
	defer srv.Close()
	c, err := NewIntesi(IntesiConfig{BaseURL: srv.URL, ClientID: "id", ClientSecret: "s", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Validate(context.Background(), ValidateReq{CertPEM: "x"})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v (resp %+v), want ErrRejected", err, got)
	}
}

func TestSwisscom_4xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"Result": map[string]string{"ResultMajor": "urn:oasis:names:tc:dss:1.0:resultmajor:Success"}})
	}))
	defer srv.Close()
	c, err := NewSwisscom(SwisscomConfig{BaseURL: srv.URL, CustomerID: "cust", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Validate(context.Background(), ValidateReq{CertPEM: "x"})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v (resp %+v), want ErrRejected", err, got)
	}
}
