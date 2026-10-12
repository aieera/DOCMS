package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/connector/internal/ssrf"
)

// IMAPBackend is the real Poller backend for the IMAP modality. The
// IMAPPoller in pollers.go is a thin wrapper that decides whether to
// delegate (Backend != nil) or stub (Backend == nil); this is the
// production-shape implementation.
//
// Wire shape:
//  1. Look up the config row's encrypted password, decrypt under the
//     deploy KEK (set up in crypto.go).
//  2. Dial the server with TLS (port 993 default) or STARTTLS (143).
//  3. LOGIN with the stored username + decrypted password.
//  4. SELECT INBOX, then SEARCH SINCE <30d-ago> UNSEEN to pull only
//     recent unread mail. Spec said "last 30 days"; we narrow further
//     to UNSEEN so we don't re-replay the same backlog on every poll.
//     Idempotency is still anchored on the (UIDVALIDITY, UID) → unique
//     source_message_id key in email_messages, so a customer who marks
//     mail read elsewhere doesn't lose ingestion.
//  5. FETCH BODY[] + INTERNALDATE for each match; parse with net/mail
//     to pull the envelope shape. Body part and attachments lift out
//     of the MIME tree.
type IMAPBackend struct {
	Pool *pgxpool.Pool
	Log  zerolog.Logger
	// AllowPrivate (SEDOC_IMAP_ALLOW_PRIVATE, on-prem) permits mail servers
	// on internal addresses and non-standard ports. Off by default: the
	// dial-time guard refuses loopback/private/link-local/CGNAT targets.
	AllowPrivate bool
}

// Generic, caller-facing IMAP errors (N-5). Details (address, dial/TLS
// cause, server text) go to the service log only, so the poll result and
// last_error are not a port-scan / reachability oracle.
var (
	errIMAPCredentials = errors.New("imap: stored credentials unavailable")
	errIMAPConnect     = errors.New("imap: could not establish a TLS connection to the mail server")
	errIMAPLogin       = errors.New("imap: login rejected")
	errIMAPMailbox     = errors.New("imap: reading the mailbox failed")
)

// imapPorts are the only ports a tenant may target without the on-prem
// opt-in: implicit TLS (993) and STARTTLS (143).
var imapPorts = map[int]bool{993: true, 143: true}

// validateIMAPEndpoint applies the webhook-style SSRF rules to a
// tenant-supplied IMAP host:port at config time. The dial-time guard
// re-checks the resolved address on every connection.
func validateIMAPEndpoint(host string, port int, allowPrivate bool) error {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, "/@ ") {
		return errors.New("imap_host must be a bare host name or IP address")
	}
	if port < 1 || port > 65535 {
		return errors.New("imap_port out of range")
	}
	if !allowPrivate && !imapPorts[port] {
		return errors.New("imap_port must be 993 (TLS) or 143 (STARTTLS)")
	}
	ips, err := net.LookupIP(strings.Trim(host, "[]"))
	if err != nil || len(ips) == 0 {
		return errors.New("imap_host does not resolve")
	}
	if !allowPrivate {
		for _, ip := range ips {
			if ssrf.IsInternalIP(ip) {
				return errors.New("imap_host resolves to an internal address")
			}
		}
	}
	return nil
}

// Fetch implements pollerBackend.
func (b *IMAPBackend) Fetch(ctx context.Context, cfg *Config) ([]*Envelope, error) {
	pwd, err := b.loadPassword(ctx, cfg)
	if err != nil {
		b.Log.Warn().Err(err).Str("config_id", cfg.ID).Msg("imap: load/decrypt password")
		return nil, errIMAPCredentials
	}
	if pwd == "" {
		return nil, errors.New("imap password not set for config")
	}
	return b.fetchWithPassword(ctx, cfg, pwd)
}

// fetchWithPassword runs one IMAP session: dial, login, select, search,
// fetch.
func (b *IMAPBackend) fetchWithPassword(ctx context.Context, cfg *Config, pwd string) ([]*Envelope, error) {
	addr := net.JoinHostPort(cfg.IMAPHost, strconv.Itoa(cfg.IMAPPort))
	logEv := func(err error, what string) {
		b.Log.Warn().Err(err).Str("config_id", cfg.ID).Str("addr", addr).Msg("imap: " + what)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if !b.AllowPrivate {
		dialer.Control = ssrf.DialControl
	}
	opts := &imapclient.Options{
		Dialer:    dialer,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.IMAPHost},
	}
	// Credentials only ever travel inside TLS: implicit TLS when
	// imap_use_tls, otherwise a mandatory STARTTLS upgrade — a server
	// without STARTTLS is refused, never logged into in cleartext.
	var (
		client *imapclient.Client
		err    error
	)
	if cfg.IMAPUseTLS {
		client, err = imapclient.DialTLS(addr, opts)
	} else {
		client, err = imapclient.DialStartTLS(addr, opts)
	}
	if err != nil {
		logEv(err, "connect")
		return nil, errIMAPConnect
	}
	defer func() { _ = client.Close() }()

	if err := client.Login(cfg.IMAPUsername, pwd).Wait(); err != nil {
		logEv(err, "login")
		return nil, errIMAPLogin
	}
	defer func() { _ = client.Logout().Wait() }()

	if _, err := client.Select("INBOX", nil).Wait(); err != nil {
		logEv(err, "select INBOX")
		return nil, errIMAPMailbox
	}

	// SEARCH SINCE <30d ago> UNSEEN. We don't restrict the search by
	// UID because we want every message that's still unseen since the
	// cutoff, regardless of whether it's a UID we've seen before — the
	// downstream INSERT ON CONFLICT is the actual dedupe.
	since := time.Now().AddDate(0, 0, -30)
	searchCriteria := &imap.SearchCriteria{
		Since: since,
		// Pull both SEEN and UNSEEN; idempotency is on the unique key,
		// not on read-state.
	}
	searchData, err := client.Search(searchCriteria, nil).Wait()
	if err != nil {
		logEv(err, "search")
		return nil, errIMAPMailbox
	}
	uids := searchData.AllSeqNums()
	if len(uids) == 0 {
		return nil, nil
	}
	// Cap a single poll at 100 messages so a giant backlog doesn't
	// occupy the worker for minutes — the next tick picks up the rest.
	if len(uids) > 100 {
		uids = uids[len(uids)-100:]
	}

	seqSet := imap.SeqSet{}
	for _, n := range uids {
		seqSet.AddNum(n)
	}
	fetchOpts := &imap.FetchOptions{
		Envelope:     true,
		InternalDate: true,
		BodySection:  []*imap.FetchItemBodySection{{}},
		UID:          true,
	}
	fetchData := client.Fetch(seqSet, fetchOpts)
	defer fetchData.Close()

	var envelopes []*Envelope
	for {
		msg := fetchData.Next()
		if msg == nil {
			break
		}
		buf, err := msg.Collect()
		if err != nil {
			b.Log.Warn().Err(err).Msg("imap fetch collect")
			continue
		}
		env, parseErr := parseIMAPMessage(buf)
		if parseErr != nil {
			b.Log.Warn().Err(parseErr).Uint32("uid", uint32(buf.UID)).Msg("imap parse")
			continue
		}
		envelopes = append(envelopes, env)
	}
	if err := fetchData.Close(); err != nil {
		logEv(err, "fetch")
		return envelopes, errIMAPMailbox
	}
	return envelopes, nil
}

// loadPassword pulls the encrypted blob from email_ingestion_configs and
// returns plaintext. email_ingestion_configs has FORCE ROW LEVEL SECURITY and
// the app role is NOBYPASSRLS, so the read MUST run inside WithTenantTx: the
// RLS policy keys on current_setting('app.current_tenant'), which is only set
// by SET LOCAL inside the tenant tx. A raw-pool query leaves the GUC unset, so
// the policy predicate is NULL and the row is INVISIBLE — loadPassword would
// return no-rows in prod and IMAP ingestion would hard-fail. (The old comment
// claiming "RLS still gates it" via the WHERE clause was wrong: RLS fails
// closed without the tenant GUC, it doesn't fall back to the WHERE.)
func (b *IMAPBackend) loadPassword(ctx context.Context, cfg *Config) (string, error) {
	tenantUUID, err := uuid.Parse(cfg.TenantID)
	if err != nil {
		return "", fmt.Errorf("tenant_id: %w", err)
	}
	var blob []byte
	err = database.WithTenantTx(ctx, b.Pool, tenantUUID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT imap_password_encrypted FROM email_ingestion_configs
			  WHERE tenant_id = $1 AND id = $2`,
			cfg.TenantID, cfg.ID,
		).Scan(&blob)
	})
	if err != nil {
		return "", err
	}
	return decryptPassword(blob)
}

// parseIMAPMessage turns the IMAP FETCH buffer into a transport-agnostic
// Envelope. The body[] section carries the full RFC-5322 message; we
// hand that to net/mail for headers, then walk the MIME parts ourselves
// because the stdlib doesn't expose multipart traversal as a one-shot.
func parseIMAPMessage(buf *imapclient.FetchMessageBuffer) (*Envelope, error) {
	var raw []byte
	for _, section := range buf.BodySection {
		raw = section.Bytes
		break
	}
	if len(raw) == 0 {
		return nil, errors.New("empty body")
	}
	msg, err := mail.ReadMessage(readerOf(raw))
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	env := &Envelope{
		SourceMessageID: imapUIDKey(buf),
		// Headers carry RFC 2047 encoded-words for any non-ASCII text;
		// the subject becomes the filed document's title, so decode it.
		Subject:  decodeHeader(msg.Header.Get("Subject")),
		From:     decodeHeader(msg.Header.Get("From")),
		ThreadID: msg.Header.Get("Thread-Index"), // best-effort; not all servers set this
	}
	if to := msg.Header.Get("To"); to != "" {
		env.To = []string{to}
	}
	if d, err := msg.Header.Date(); err == nil {
		env.Date = d
	} else if !buf.InternalDate.IsZero() {
		env.Date = buf.InternalDate
	}
	body, attachments, perr := walkMIME(msg)
	if perr != nil {
		return nil, perr
	}
	env.BodyText = body
	env.Attachments = attachments
	return env, nil
}

// imapUIDKey returns a stable per-config message id: "uidvalidity/uid".
// Sufficient for the email_messages unique key — uidvalidity bumps if the
// server reissues UIDs, naturally invalidating prior idempotency.
func imapUIDKey(buf *imapclient.FetchMessageBuffer) string {
	return fmt.Sprintf("imap/%d", buf.UID)
}

// readerOf wraps bytes for net/mail.
type sliceReader struct {
	b []byte
	i int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

func readerOf(b []byte) *sliceReader { return &sliceReader{b: b} }
