package email

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"sort"
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
//     deploy KEK (crypto.go).
//  2. Dial through the SSRF guard with implicit TLS (993) or mandatory
//     STARTTLS (143); never cleartext.
//  3. LOGIN with the stored username + decrypted password.
//  4. SELECT INBOX (→ UIDVALIDITY), then UID SEARCH SINCE <30d> for UIDs
//     above the config's high-water mark, oldest first, plus a bounded set
//     of failed messages to retry; at most 100 per poll.
//  5. UID FETCH BODY[] + INTERNALDATE; parse with net/mail and walk the
//     MIME tree. Each message is keyed "imap/<uidvalidity>/<uid>".
type IMAPBackend struct {
	Pool *pgxpool.Pool
	Log  zerolog.Logger
	// AllowPrivate (SEDOC_IMAP_ALLOW_PRIVATE, on-prem) permits mail servers
	// on internal addresses and non-standard ports. Off by default: the
	// dial-time guard refuses loopback/private/link-local/CGNAT targets.
	AllowPrivate bool
	// tlsRoots overrides the system roots (tests only).
	tlsRoots *x509.CertPool
	// maxMessageBytes caps one message (default defaultMaxMessageBytes).
	maxMessageBytes int64
	// collect reads one FETCH response (msg.Collect when nil; tests inject
	// failures).
	collect func(*imapclient.FetchMessageData) (*imapclient.FetchMessageBuffer, error)
}

// defaultMaxMessageBytes caps a single fetched message (N-9). Larger
// messages are recorded as a permanent failure, never downloaded whole.
const defaultMaxMessageBytes = 50 << 20

// permanentPrefix marks an ingest_error that retrying cannot fix (too
// large, unparseable); such rows are excluded from the retry set.
const permanentPrefix = "permanent: "

func (b *IMAPBackend) maxBytes() int64 {
	if b.maxMessageBytes > 0 {
		return b.maxMessageBytes
	}
	return defaultMaxMessageBytes
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
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.IMAPHost, RootCAs: b.tlsRoots},
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

	sel, err := client.Select("INBOX", nil).Wait()
	if err != nil {
		logEv(err, "select INBOX")
		return nil, errIMAPMailbox
	}
	uv := sel.UIDValidity

	// N-7: page oldest-first from the per-config high-water UID for this
	// UIDVALIDITY (derived from the email_messages rows already recorded),
	// plus a bounded retry set of failed / abandoned messages. The old
	// newest-100-by-sequence window meant a backlog larger than 100 was
	// never ingested, and keys without UIDVALIDITY collided after a
	// mailbox re-creation.
	cur, err := b.loadCursor(ctx, cfg, uv)
	if err != nil {
		logEv(err, "load ingestion cursor")
		return nil, errIMAPCursor
	}
	since := time.Now().AddDate(0, 0, -30)
	var above imap.UIDSet
	above.AddRange(imap.UID(cur.highWater+1), 0) // hw+1:*
	searchData, err := client.UIDSearch(&imap.SearchCriteria{Since: since, UID: []imap.UIDSet{above}}, nil).Wait()
	if err != nil {
		logEv(err, "search")
		return nil, errIMAPMailbox
	}
	var fresh []imap.UID
	for _, u := range searchData.AllUIDs() {
		// "n:*" also matches the highest UID when n is past it.
		if uint32(u) > cur.highWater {
			fresh = append(fresh, u)
		}
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i] < fresh[j] })
	if cur.legacyApplies && len(fresh) > 0 {
		done, err := b.legacyMaterialised(ctx, cfg, fresh)
		if err != nil {
			logEv(err, "legacy key lookup")
			return nil, errIMAPCursor
		}
		kept := fresh[:0]
		for _, u := range fresh {
			if !done[u] {
				kept = append(kept, u)
			}
		}
		fresh = kept
	}
	budget := imapPollBatch - len(cur.retry)
	if len(fresh) > budget {
		fresh = fresh[:budget]
	}
	uids := append(append([]imap.UID{}, cur.retry...), fresh...)
	if len(uids) == 0 {
		return nil, nil
	}
	uidSet := imap.UIDSetNum(uids...)
	// RFC822.SIZE classifies oversize mail; the body section is requested
	// with a partial range so even a server that under-reports the size
	// cannot make us buffer more than the cap.
	maxBytes := b.maxBytes()
	fetchOpts := &imap.FetchOptions{
		Envelope:     true,
		InternalDate: true,
		RFC822Size:   true,
		BodySection:  []*imap.FetchItemBodySection{{Peek: true, Partial: &imap.SectionPartial{Offset: 0, Size: maxBytes + 1}}},
		UID:          true,
	}
	fetchData := client.Fetch(uidSet, fetchOpts)
	defer fetchData.Close()

	var envelopes []*Envelope
	returned := map[imap.UID]bool{}
	for {
		msg := fetchData.Next()
		if msg == nil {
			break
		}
		var buf *imapclient.FetchMessageBuffer
		if b.collect != nil {
			buf, err = b.collect(msg)
		} else {
			buf, err = msg.Collect()
		}
		if err != nil {
			b.Log.Warn().Err(err).Msg("imap fetch collect")
			continue
		}
		returned[buf.UID] = true
		key := imapKey(uv, buf.UID)
		if buf.RFC822Size > maxBytes {
			envelopes = append(envelopes, &Envelope{SourceMessageID: key,
				Err: fmt.Sprintf("%smessage is %d bytes, over the %d-byte cap", permanentPrefix, buf.RFC822Size, maxBytes)})
			continue
		}
		env, parseErr := parseIMAPMessage(buf, uv, maxBytes)
		if errors.Is(parseErr, errMessageTooLarge) {
			envelopes = append(envelopes, &Envelope{SourceMessageID: key,
				Err: fmt.Sprintf("%smessage is over the %d-byte cap (server reported %d bytes)", permanentPrefix, maxBytes, buf.RFC822Size)})
			continue
		}
		if parseErr != nil {
			b.Log.Warn().Err(parseErr).Uint32("uid", uint32(buf.UID)).Msg("imap parse")
			// Recorded (not skipped) so the high-water mark moves past it
			// and the failure is visible in the config stats.
			envelopes = append(envelopes, &Envelope{SourceMessageID: key,
				Err: permanentPrefix + "unparseable message: " + parseErr.Error()})
			continue
		}
		envelopes = append(envelopes, env)
	}
	if err := fetchData.Close(); err != nil {
		logEv(err, "fetch")
		return envelopes, errIMAPMailbox
	}
	// N-7: every requested UID that produced no usable FETCH response (its
	// Collect failed, or the server returned nothing for it) is recorded,
	// never silently skipped — otherwise the high-water mark moves past it
	// and it is lost. A UID still on the server is a retryable failure; one
	// the server no longer has (expunged) is permanent, so it cannot occupy
	// the retry set forever.
	var missing []imap.UID
	for _, u := range uids {
		if !returned[u] {
			missing = append(missing, u)
		}
	}
	if len(missing) > 0 {
		still := map[imap.UID]bool{}
		sd, err := client.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{imap.UIDSetNum(missing...)}}, nil).Wait()
		if err != nil {
			logEv(err, "re-check unfetched messages")
			for _, u := range missing { // unknown: keep them retryable
				still[u] = true
			}
		} else {
			for _, u := range sd.AllUIDs() {
				still[u] = true
			}
		}
		for _, u := range missing {
			msg := "imap: message could not be fetched; will retry"
			if !still[u] {
				msg = permanentPrefix + "message no longer on the server"
			}
			envelopes = append(envelopes, &Envelope{SourceMessageID: imapKey(uv, u), Err: msg})
		}
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
// errMessageTooLarge: the fetched body is longer than the cap — the server
// under-reported RFC822.SIZE and the partial fetch (cap+1 bytes) cut it off.
var errMessageTooLarge = errors.New("message exceeds the size cap")

func parseIMAPMessage(buf *imapclient.FetchMessageBuffer, uidValidity uint32, maxBytes int64) (*Envelope, error) {
	var raw []byte
	for _, section := range buf.BodySection {
		raw = section.Bytes
		break
	}
	if len(raw) == 0 {
		return nil, errors.New("empty body")
	}
	// The body is fetched as a partial range of maxBytes+1: reaching past
	// maxBytes means the message is bigger than the cap (whatever
	// RFC822.SIZE said) and what we hold is truncated — fail it rather than
	// file a cut-off message as complete.
	if int64(len(raw)) > maxBytes {
		return nil, errMessageTooLarge
	}
	msg, err := mail.ReadMessage(io.LimitReader(readerOf(raw), maxBytes))
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	env := &Envelope{
		SourceMessageID: imapKey(uidValidity, buf.UID),
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

// imapKey is the per-config message id: "imap/<uidvalidity>/<uid>". A
// UID is only unique within one UIDVALIDITY; when the server re-creates the
// mailbox, UIDs restart and the new UIDVALIDITY keeps the keys distinct
// (N-7). Rows written before this change use the legacy "imap/<uid>".
func imapKey(uidValidity uint32, uid imap.UID) string {
	return fmt.Sprintf("imap/%d/%d", uidValidity, uid)
}

// One poll fetches at most imapPollBatch messages, of which up to
// imapRetryBatch are retries of failed / abandoned ones.
const (
	imapPollBatch  = 100
	imapRetryBatch = 20
)

var errIMAPCursor = errors.New("imap: loading the ingestion cursor failed")

// imapCursor is where a poll resumes for one config + UIDVALIDITY.
type imapCursor struct {
	highWater uint32     // highest UID already recorded under this UIDVALIDITY
	retry     []imap.UID // failed or lease-expired pending messages to re-fetch
	// legacyApplies: rows keyed "imap/<uid>" (pre-N-7) still describe this
	// mailbox — true until a key with another UIDVALIDITY shows up.
	legacyApplies bool
}

func (b *IMAPBackend) loadCursor(ctx context.Context, cfg *Config, uidValidity uint32) (*imapCursor, error) {
	tenantUUID, err := uuid.Parse(cfg.TenantID)
	if err != nil {
		return nil, err
	}
	prefix := fmt.Sprintf("imap/%d/", uidValidity)
	cur := &imapCursor{}
	err = database.WithTenantTx(ctx, b.Pool, tenantUUID, func(tx pgx.Tx) error {
		var hw int64
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(max(split_part(source_message_id, '/', 3)::bigint), 0)
			  FROM email_messages
			 WHERE tenant_id = $1 AND config_id = $2 AND source_message_id LIKE $3`,
			cfg.TenantID, cfg.ID, prefix+"%").Scan(&hw); err != nil {
			return err
		}
		cur.highWater = uint32(hw)
		rows, err := tx.Query(ctx, `
			SELECT split_part(source_message_id, '/', 3)::bigint
			  FROM email_messages
			 WHERE tenant_id = $1 AND config_id = $2 AND source_message_id LIKE $3
			   AND ((ingest_status = 'failed' AND COALESCE(ingest_error, '') NOT LIKE 'permanent:%')
			        OR (ingest_status = 'pending' AND ingested_at < now() - make_interval(secs => $4)))
			 ORDER BY ingested_at
			 LIMIT $5`,
			cfg.TenantID, cfg.ID, prefix+"%", messageLease.Seconds(), imapRetryBatch)
		if err != nil {
			return err
		}
		for rows.Next() {
			var u int64
			if err := rows.Scan(&u); err != nil {
				rows.Close()
				return err
			}
			cur.retry = append(cur.retry, imap.UID(u))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var otherUV bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM email_messages
			                WHERE tenant_id = $1 AND config_id = $2
			                  AND source_message_id LIKE 'imap/%/%'
			                  AND source_message_id NOT LIKE $3)`,
			cfg.TenantID, cfg.ID, prefix+"%").Scan(&otherUV); err != nil {
			return err
		}
		cur.legacyApplies = !otherUV
		return nil
	})
	return cur, err
}

// legacyMaterialised returns which of uids were already materialised under
// the pre-N-7 key "imap/<uid>", so the key change does not re-ingest mail
// the old poller already filed.
func (b *IMAPBackend) legacyMaterialised(ctx context.Context, cfg *Config, uids []imap.UID) (map[imap.UID]bool, error) {
	tenantUUID, err := uuid.Parse(cfg.TenantID)
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(uids))
	for i, u := range uids {
		keys[i] = fmt.Sprintf("imap/%d", u)
	}
	done := map[imap.UID]bool{}
	err = database.WithTenantTx(ctx, b.Pool, tenantUUID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT split_part(source_message_id, '/', 2)::bigint
			  FROM email_messages
			 WHERE tenant_id = $1 AND config_id = $2
			   AND ingest_status = 'materialised'
			   AND source_message_id = ANY($3)`,
			cfg.TenantID, cfg.ID, keys)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u int64
			if err := rows.Scan(&u); err != nil {
				return err
			}
			done[imap.UID(u)] = true
		}
		return rows.Err()
	})
	return done, err
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
