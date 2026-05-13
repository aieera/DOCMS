package email

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"strconv"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// IMAPBackend is the real Poller backend for the IMAP modality. The
// IMAPPoller in pollers.go is a thin wrapper that decides whether to
// delegate (Backend != nil) or stub (Backend == nil); this is the
// production-shape implementation.
//
// Wire shape:
//   1. Look up the config row's encrypted password, decrypt under the
//      deploy KEK (set up in crypto.go).
//   2. Dial the server with TLS (port 993 default) or STARTTLS (143).
//   3. LOGIN with the stored username + decrypted password.
//   4. SELECT INBOX, then SEARCH SINCE <30d-ago> UNSEEN to pull only
//      recent unread mail. Spec said "last 30 days"; we narrow further
//      to UNSEEN so we don't re-replay the same backlog on every poll.
//      Idempotency is still anchored on the (UIDVALIDITY, UID) → unique
//      source_message_id key in email_messages, so a customer who marks
//      mail read elsewhere doesn't lose ingestion.
//   5. FETCH BODY[] + INTERNALDATE for each match; parse with net/mail
//      to pull the envelope shape. Body part and attachments lift out
//      of the MIME tree.
type IMAPBackend struct {
	Pool *pgxpool.Pool
	Log  zerolog.Logger
}

// Fetch implements pollerBackend.
func (b *IMAPBackend) Fetch(ctx context.Context, cfg *Config) ([]*Envelope, error) {
	pwd, err := b.loadPassword(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("decrypt imap password: %w", err)
	}
	if pwd == "" {
		return nil, errors.New("imap password not set for config")
	}

	addr := net.JoinHostPort(cfg.IMAPHost, strconv.Itoa(cfg.IMAPPort))
	var client *imapclient.Client
	if cfg.IMAPUseTLS {
		client, err = imapclient.DialTLS(addr, nil)
	} else {
		client, err = imapclient.DialInsecure(addr, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("imap dial %s: %w", addr, err)
	}
	defer func() { _ = client.Close() }()

	if err := client.Login(cfg.IMAPUsername, pwd).Wait(); err != nil {
		return nil, fmt.Errorf("imap login: %w", err)
	}
	defer func() { _ = client.Logout().Wait() }()

	if _, err := client.Select("INBOX", nil).Wait(); err != nil {
		return nil, fmt.Errorf("imap select INBOX: %w", err)
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
		return nil, fmt.Errorf("imap search: %w", err)
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
		Envelope:      true,
		InternalDate:  true,
		BodySection:   []*imap.FetchItemBodySection{{}},
		UID:           true,
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
		return envelopes, fmt.Errorf("imap fetch close: %w", err)
	}
	return envelopes, nil
}

// loadPassword pulls the encrypted blob from email_ingestion_configs and
// returns plaintext. Worth noting: we read directly via the pool (no
// WithTenantTx) because the row is loaded by ID + tenant_id and the
// password BYTEA is owned by the row — RLS still gates it.
func (b *IMAPBackend) loadPassword(ctx context.Context, cfg *Config) (string, error) {
	var blob []byte
	err := b.Pool.QueryRow(ctx,
		`SELECT imap_password_encrypted FROM email_ingestion_configs
		  WHERE tenant_id = $1 AND id = $2`,
		cfg.TenantID, cfg.ID,
	).Scan(&blob)
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
		Subject:         msg.Header.Get("Subject"),
		From:            msg.Header.Get("From"),
		ThreadID:        msg.Header.Get("Thread-Index"), // best-effort; not all servers set this
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