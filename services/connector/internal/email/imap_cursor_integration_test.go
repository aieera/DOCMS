//go:build integration && prodposture
// +build integration,prodposture

// N-7: IMAP ingestion keys on uidvalidity/uid and pages oldest-first from a
// per-config high-water UID, so a backlog larger than one poll is fully
// ingested and a mailbox whose UIDVALIDITY changed keeps ingesting new
// mail. Real IMAP over TLS (in-process go-imap server), real schema of
// record (document 000041), NOBYPASSRLS.
package email

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

type testIMAP struct {
	user  *imapmemserver.User
	port  int
	roots *x509.CertPool
}

func startTestIMAP(t *testing.T) *testIMAP {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "imap-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}

	mem := imapmemserver.New()
	user := imapmemserver.NewUser("alice", "s3cret")
	require.NoError(t, user.Create("INBOX", nil))
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:      imap.CapSet{imap.CapIMAP4rev1: {}},
		TLSConfig: tlsCfg,
	})
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &testIMAP{user: user, port: ln.Addr().(*net.TCPAddr).Port, roots: roots}
}

func (s *testIMAP) appendMail(t *testing.T, subject string) {
	t.Helper()
	raw := fmt.Sprintf("From: a@example.test\r\nTo: b@example.test\r\nSubject: %s\r\nDate: %s\r\nContent-Type: text/plain\r\n\r\nbody of %s\r\n",
		subject, time.Now().Format(time.RFC1123Z), subject)
	_, err := s.user.Append("INBOX", bytes.NewReader([]byte(raw)), &imap.AppendOptions{Time: time.Now()})
	require.NoError(t, err)
}

func newIMAPPollTest(ctx context.Context, t *testing.T) (*emailFixture, *testIMAP, *testService, *countingIngester) {
	t.Helper()
	t.Setenv("SEDOC_LOCAL_KEK", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	fx := newEmailFixture(ctx, t, SourceIMAP)
	srv := startTestIMAP(t)
	pwd, err := encryptPassword("s3cret")
	require.NoError(t, err)
	require.NoError(t, database.WithTenantTx(ctx, fx.db.App, fx.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE email_ingestion_configs
			   SET imap_host = '127.0.0.1', imap_port = $3, imap_use_tls = TRUE,
			       imap_username = 'alice', imap_password_encrypted = $4
			 WHERE tenant_id = $1 AND id = $2`, fx.tenant, fx.cfgID, srv.port, pwd)
		return err
	}))
	backend := &IMAPBackend{Pool: fx.db.App, Log: zerolog.Nop(), AllowPrivate: true, tlsRoots: srv.roots}
	ing := newCountingIngester()
	svc := newTestService(fx, &IMAPPoller{Backend: backend}, ing)
	return fx, srv, svc, ing
}

func TestIMAP_OldMessagesEventuallyIngested(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	_, srv, svc, ing := newIMAPPollTest(ctx, t)

	const total = 130 // more than one poll's worth
	for i := 1; i <= total; i++ {
		srv.appendMail(t, fmt.Sprintf("m%03d", i))
	}
	for run := 0; run < 3; run++ {
		_, err := svc.RunOnce(ctx, svc.testTenant, svc.testConfig)
		require.NoError(t, err)
	}
	for i := 1; i <= total; i++ {
		require.Equal(t, 1, ing.count(fmt.Sprintf("m%03d.md", i)), "message m%03d ingested once", i)
	}
}

func TestIMAP_UIDValidityChangeIngestsNewMail(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	_, srv, svc, ing := newIMAPPollTest(ctx, t)

	for _, s := range []string{"old-1", "old-2", "old-3"} {
		srv.appendMail(t, s)
	}
	_, err := svc.RunOnce(ctx, svc.testTenant, svc.testConfig)
	require.NoError(t, err)

	// The server re-creates the mailbox: UIDVALIDITY changes and UIDs
	// restart at 1, colliding with the already-ingested ones.
	require.NoError(t, srv.user.Delete("INBOX"))
	require.NoError(t, srv.user.Create("INBOX", nil))
	srv.appendMail(t, "new-1")
	srv.appendMail(t, "new-2")
	_, err = svc.RunOnce(ctx, svc.testTenant, svc.testConfig)
	require.NoError(t, err)

	require.Equal(t, 1, ing.count("new-1.md"), "new mail after a UIDVALIDITY change must be ingested")
	require.Equal(t, 1, ing.count("new-2.md"))
	require.Equal(t, 1, ing.count("old-1.md"))
}

// Upgrade path: mail the old poller already materialised under the legacy
// key "imap/<uid>" must not be ingested again under the new key.
func TestIMAP_LegacyKeysNotReingested(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	fx, srv, svc, ing := newIMAPPollTest(ctx, t)
	for i := 1; i <= 5; i++ {
		srv.appendMail(t, fmt.Sprintf("l%d", i))
	}
	require.NoError(t, database.WithTenantTx(ctx, fx.db.App, fx.tenant, func(tx pgx.Tx) error {
		for _, uid := range []int{1, 2, 3} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO email_messages (tenant_id, config_id, source_message_id, ingest_status)
				VALUES ($1, $2, $3, 'materialised')`, fx.tenant, fx.cfgID, fmt.Sprintf("imap/%d", uid)); err != nil {
				return err
			}
		}
		return nil
	}))
	_, err := svc.RunOnce(ctx, svc.testTenant, svc.testConfig)
	require.NoError(t, err)
	for i := 1; i <= 3; i++ {
		require.Equal(t, 0, ing.count(fmt.Sprintf("l%d.md", i)), "legacy-materialised l%d re-ingested", i)
	}
	require.Equal(t, 1, ing.count("l4.md"))
	require.Equal(t, 1, ing.count("l5.md"))
}
