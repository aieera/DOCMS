// Package email implements the §12.5 email-ingestion service: per-tenant
// configs, a polling worker that fans out to Microsoft Graph / Gmail /
// IMAP, and the email→document conversion path.
//
// This package glues pollers onto a tenant config, schedules polling,
// persists what was ingested, and materialises each envelope (body +
// attachments) into real documents through the shared ingest pipeline.
// IMAP has a real backend; the Microsoft and Gmail pollers are no-ops
// until a Graph / Gmail backend is wired.
package email

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/connector/internal/ingest"
)

// Source enumerates supported ingestion sources.
type Source string

const (
	SourceMicrosoft Source = "microsoft"
	SourceGmail     Source = "gmail"
	SourceIMAP      Source = "imap"
)

// Config is a per-tenant ingestion configuration.
type Config struct {
	ID                  string     `json:"id"`
	TenantID            string     `json:"tenant_id"`
	Source              Source     `json:"source"`
	Label               string     `json:"label"`
	Active              bool       `json:"active"`
	OAuthProvider       string     `json:"oauth_provider,omitempty"`
	IMAPHost            string     `json:"imap_host,omitempty"`
	IMAPPort            int        `json:"imap_port,omitempty"`
	IMAPUseTLS          bool       `json:"imap_use_tls"`
	IMAPUsername        string     `json:"imap_username,omitempty"`
	TargetWorkspaceID   string     `json:"target_workspace_id,omitempty"`
	TargetFolderID      string     `json:"target_folder_id,omitempty"`
	PollIntervalSeconds int        `json:"poll_interval_seconds"`
	LastRunAt           *time.Time `json:"last_run_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	MessagesIngested    int64      `json:"messages_ingested"`
	CreatedBy           string     `json:"created_by,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
}

// CreateConfigInput is the request body shape for POST /email-configs.
type CreateConfigInput struct {
	Source              Source `json:"source"`
	Label               string `json:"label"`
	OAuthProvider       string `json:"oauth_provider,omitempty"`
	IMAPHost            string `json:"imap_host,omitempty"`
	IMAPPort            int    `json:"imap_port,omitempty"`
	IMAPUseTLS          bool   `json:"imap_use_tls"`
	IMAPUsername        string `json:"imap_username,omitempty"`
	IMAPPassword        string `json:"imap_password,omitempty"` // plaintext at POST time only
	TargetWorkspaceID   string `json:"target_workspace_id,omitempty"`
	TargetFolderID      string `json:"target_folder_id,omitempty"`
	PollIntervalSeconds int    `json:"poll_interval_seconds,omitempty"`
}

// PatchConfigInput is the request body for PATCH /email-configs/{id}.
// All fields optional; non-nil = update.
type PatchConfigInput struct {
	Active              *bool   `json:"active,omitempty"`
	Label               *string `json:"label,omitempty"`
	TargetWorkspaceID   *string `json:"target_workspace_id,omitempty"`
	TargetFolderID      *string `json:"target_folder_id,omitempty"`
	PollIntervalSeconds *int    `json:"poll_interval_seconds,omitempty"`
}

// Stats is what GET /email-configs/{id}/stats returns.
type Stats struct {
	ConfigID        string     `json:"config_id"`
	MessagesTotal   int64      `json:"messages_total"`
	MessagesPending int64      `json:"messages_pending"`
	MessagesFailed  int64      `json:"messages_failed"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	LastSuccessAt   *time.Time `json:"last_success_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	NextRunAt       *time.Time `json:"next_run_at,omitempty"`
}

// Envelope is the source-agnostic shape providers return. Worker turns
// it into one document for the body and one document per attachment.
type Envelope struct {
	SourceMessageID string
	ThreadID        string
	Subject         string
	From            string
	To              []string
	Date            time.Time
	BodyText        string
	BodyHTML        string
	Attachments     []Attachment
	// Err, when set, records the message as failed without materialising
	// it (e.g. "permanent: message is N bytes, over the cap"). Errors with
	// the "permanent: " prefix are not retried.
	Err string
}

// Attachment carries the raw bytes plus the bookkeeping the document
// service needs to file it.
type Attachment struct {
	Filename    string
	ContentType string
	Bytes       []byte
}

// Poller is the per-source contract implemented by Microsoft, Gmail,
// and IMAP adapters. Returning (nil, nil) is valid — empty inbox.
type Poller interface {
	Source() Source
	Poll(ctx context.Context, cfg *Config) ([]*Envelope, error)
}

// ingester is the slice of *ingest.Client materialisation needs.
type ingester interface {
	IngestFile(ctx context.Context, tenantID, actorID, authToken, workspaceID, folderID, filename, contentType string, data []byte, customMetadata map[string]any) (string, error)
}

// Service orchestrates config CRUD + polling.
type Service struct {
	pool    *pgxpool.Pool
	nc      *nats.Conn
	ingest  ingester
	log     zerolog.Logger
	pollers map[Source]Poller
	mu      sync.Mutex
	// allowPrivateIMAP mirrors IMAPBackend.AllowPrivate for config-time
	// validation (SEDOC_IMAP_ALLOW_PRIVATE).
	allowPrivateIMAP bool
}

// SetAllowPrivateIMAP enables the on-prem opt-in that permits IMAP servers
// on internal addresses / non-standard ports.
func (s *Service) SetAllowPrivateIMAP(v bool) { s.allowPrivateIMAP = v }

// New constructs a Service. Pollers map keyed by Source so adding a
// new modality (Exchange on-prem, ProtonMail Bridge) is a no-touch
// change to this file. ingestClient materialises each envelope (body +
// attachments) into real documents-with-versions via the shared ingest
// pipeline — when nil, the worker still records ingestion in
// email_messages but skips the document side (e.g. CI / storage down).
func New(pool *pgxpool.Pool, nc *nats.Conn, ingestClient *ingest.Client, pollers []Poller, log zerolog.Logger) *Service {
	m := map[Source]Poller{}
	for _, p := range pollers {
		m[p.Source()] = p
	}
	s := &Service{pool: pool, nc: nc, log: log, pollers: m}
	// Keep a nil interface (not one wrapping a nil pointer) when storage is
	// unreachable, so the materialise nil guard still fires.
	if ingestClient != nil {
		s.ingest = ingestClient
	}
	return s
}

// withTenant opens a tenant-scoped transaction — the A.1.a template.
func (s *Service) withTenant(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	return database.WithTenantTx(ctx, s.pool, tid, fn)
}

// ---- Worker --------------------------------------------------------------

// Start runs the dispatcher loop. Ticks every 30s, claims due configs,
// calls the matching poller, persists the result, and emits one NATS
// message per envelope so the document service can materialise.
func (s *Service) Start(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	// Eager first run so a fresh boot doesn't wait 30s before polling.
	s.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Service) tick(ctx context.Context) {
	configs, err := s.claimDueConfigs(ctx)
	if err != nil {
		s.log.Error().Err(err).Msg("email: dueConfigs")
		return
	}
	for _, cfg := range configs {
		s.runConfig(ctx, cfg)
	}
}

// messageLease is how long a 'pending' email_messages row belongs to the
// worker that claimed it; after that a crashed worker's message is
// re-claimable.
const messageLease = 15 * time.Minute

// claimDueConfigs claims active configs whose last_run_at + poll_interval
// is in the past (NULL last_run_at counts as due) by stamping last_run_at
// in the same UPDATE ... RETURNING, so with several connector replicas
// each due config is polled by exactly one of them (N-4). Legitimately
// cross-tenant, so it uses the sanctioned enumerate-tenants shape (Wave
// A.1, issue #71).
func (s *Service) claimDueConfigs(ctx context.Context) ([]*Config, error) {
	var out []*Config
	err := database.ForEachTenant(ctx, s.pool, func(tenantID uuid.UUID, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
		UPDATE email_ingestion_configs
		   SET last_run_at = now()
		 WHERE tenant_id = $1
		   AND active = TRUE
		   AND (last_run_at IS NULL
		        OR last_run_at + (poll_interval_seconds || ' seconds')::interval <= now())
		RETURNING id::text, tenant_id::text, source, label, active,
		       COALESCE(oauth_provider, ''),
		       COALESCE(imap_host, ''), COALESCE(imap_port, 0), imap_use_tls,
		       COALESCE(imap_username, ''),
		       COALESCE(target_workspace_id::text, ''),
		       COALESCE(target_folder_id::text, ''),
		       poll_interval_seconds, last_run_at, last_success_at,
		       COALESCE(last_error, ''), messages_ingested,
		       COALESCE(created_by::text, ''), created_at`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c := &Config{}
			if err := rows.Scan(&c.ID, &c.TenantID, &c.Source, &c.Label, &c.Active,
				&c.OAuthProvider, &c.IMAPHost, &c.IMAPPort, &c.IMAPUseTLS,
				&c.IMAPUsername, &c.TargetWorkspaceID, &c.TargetFolderID,
				&c.PollIntervalSeconds, &c.LastRunAt, &c.LastSuccessAt,
				&c.LastError, &c.MessagesIngested, &c.CreatedBy, &c.CreatedAt); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) runConfig(ctx context.Context, cfg *Config) {
	poller, ok := s.pollers[cfg.Source]
	if !ok {
		s.recordError(ctx, cfg, "unsupported source: "+string(cfg.Source))
		return
	}
	envelopes, err := poller.Poll(ctx, cfg)
	if err != nil {
		s.recordError(ctx, cfg, err.Error())
		return
	}
	ingested, err := s.persistEnvelopes(ctx, cfg, envelopes)
	if err != nil {
		s.recordError(ctx, cfg, err.Error())
		return
	}
	s.recordSuccess(ctx, cfg, ingested)
}

func (s *Service) persistEnvelopes(ctx context.Context, cfg *Config, envelopes []*Envelope) (int, error) {
	if len(envelopes) == 0 {
		return 0, nil
	}
	tenantUUID, err := uuid.Parse(cfg.TenantID)
	if err != nil {
		return 0, err
	}
	configUUID, err := uuid.Parse(cfg.ID)
	if err != nil {
		return 0, err
	}
	// Phase 1 (in tx): record each message as 'pending' and collect the ones
	// to materialise. We do NOT call the ingest pipeline inside the tx — it
	// makes storage gRPC + presigned-PUT round-trips that would hold a DB
	// connection open for seconds per message.
	//
	// The upsert is also the message lease (N-4): it returns a row — and so
	// claims the message for this worker — only for a new message, a
	// 'failed' one, or a 'pending' one whose claim (ingested_at) is older
	// than messageLease (its worker died). A 'materialised' row, or one
	// another worker claimed moments ago, yields nothing and is skipped, so
	// overlapping polls of the same mailbox materialise each message once.
	// Retrying failed/abandoned messages is the only retry path: the poller
	// re-FETCHes them, so the envelope is in hand.
	type pending struct {
		msgID string
		env   *Envelope
	}
	var toMaterialise []pending
	var ingested int
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		for _, env := range envelopes {
			if env.Err != "" {
				if _, err := tx.Exec(ctx, `
					INSERT INTO email_messages
					    (tenant_id, id, config_id, source_message_id, subject, sender,
					     received_at, ingest_status, ingest_error)
					VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, 'failed', $8)
					ON CONFLICT (tenant_id, config_id, source_message_id) DO UPDATE
					   SET ingest_status = 'failed', ingest_error = EXCLUDED.ingest_error
					 WHERE email_messages.ingest_status <> 'materialised'`,
					cfg.TenantID, newUUID(), configUUID, env.SourceMessageID,
					env.Subject, env.From, nullableTime(env.Date), env.Err); err != nil {
					return err
				}
				continue
			}
			recipients, _ := json.Marshal(env.To)
			var msgID string
			err := tx.QueryRow(ctx, `
				INSERT INTO email_messages
				    (tenant_id, id, config_id, source_message_id, thread_id,
				     subject, sender, recipients, received_at, ingest_status)
				VALUES ($1, $2, $3, $4, NULLIF($5, ''),
				        NULLIF($6, ''), NULLIF($7, ''), $8, $9, 'pending')
				ON CONFLICT (tenant_id, config_id, source_message_id) DO UPDATE
				   SET subject      = EXCLUDED.subject,
				       sender       = EXCLUDED.sender,
				       ingest_status = 'pending',
				       ingest_error  = NULL,
				       ingested_at   = now()
				 WHERE email_messages.ingest_status = 'failed'
				    OR (email_messages.ingest_status = 'pending'
				        AND email_messages.ingested_at < now() - make_interval(secs => $10))
				RETURNING id`,
				cfg.TenantID, newUUID(), configUUID, env.SourceMessageID, env.ThreadID,
				env.Subject, env.From, recipients, nullableTime(env.Date), messageLease.Seconds(),
			).Scan(&msgID)
			if errors.Is(err, pgx.ErrNoRows) {
				continue // already materialised — nothing to redo
			}
			if err != nil {
				return err
			}
			toMaterialise = append(toMaterialise, pending{msgID, env})
			ingested++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	// Phase 2 (outside tx): ingest body + attachments into real documents.
	// Best-effort per message — a failure leaves the row 'pending' (re-tried
	// by a future sweep) or stamps 'failed'; it never blocks the others.
	if s.ingest != nil {
		for _, p := range toMaterialise {
			s.materialiseEmail(ctx, cfg, p.msgID, p.env)
		}
	}
	return ingested, err
}

// materialiseEmail ingests one envelope's body + attachments as real
// documents (each gets a version → OCR + embed + index) and stamps the
// email_messages row with the outcome.
//
// N-8: materialisation is per part and resumable. Part 0 is the body
// (email_messages.document_id), part i is attachment i
// (attachment_document_ids[i]). Each part's document id is stamped as soon
// as it is filed, and parts already stamped are skipped, so a retry after
// a failure — or after a crash / failed final stamp — resumes instead of
// re-filing the body and earlier attachments. Any part failing fails the
// whole message (it is retried on a later poll); it is 'materialised' only
// when every part is filed.
func (s *Service) materialiseEmail(ctx context.Context, cfg *Config, msgID string, env *Envelope) {
	if err := s.materialiseParts(ctx, cfg, msgID, env); err != nil {
		s.log.Warn().Err(err).Str("config_id", cfg.ID).Str("message_id", msgID).Msg("email: materialise failed; will retry")
		if serr := s.withTenant(ctx, cfg.TenantID, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`UPDATE email_messages SET ingest_status = 'failed', ingest_error = $3
				  WHERE tenant_id = $1 AND id = $2`,
				cfg.TenantID, msgID, err.Error())
			return e
		}); serr != nil {
			// Row stays 'pending' and is re-claimed after messageLease.
			s.log.Error().Err(serr).Str("config_id", cfg.ID).Str("message_id", msgID).
				Msg("email: stamping failure outcome failed; message re-claimed after its lease")
		}
		return
	}
	if serr := s.withTenant(ctx, cfg.TenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`UPDATE email_messages SET ingest_status = 'materialised', ingest_error = NULL
			  WHERE tenant_id = $1 AND id = $2`,
			cfg.TenantID, msgID)
		return e
	}); serr != nil {
		// Every part is stamped already, so the retry after the lease files
		// nothing again — it only flips the status.
		s.log.Error().Err(serr).Str("config_id", cfg.ID).Str("message_id", msgID).
			Msg("email: stamping materialised failed; message re-claimed after its lease")
	}
}

// materialiseParts files every not-yet-filed part, stamping each document
// id immediately. Returns the first failure.
func (s *Service) materialiseParts(ctx context.Context, cfg *Config, msgID string, env *Envelope) error {
	if cfg.TargetWorkspaceID == "" || cfg.TargetFolderID == "" {
		return fmt.Errorf("config %s: target workspace + folder required", cfg.ID)
	}
	bodyDocID, attachDone, err := s.loadParts(ctx, cfg, msgID)
	if err != nil {
		return fmt.Errorf("load progress: %w", err)
	}
	baseMeta := map[string]any{
		"email.from":        env.From,
		"email.subject":     env.Subject,
		"email.received_at": env.Date.Format(time.RFC3339),
		"email.thread_id":   env.ThreadID,
		"email.source":      string(cfg.Source),
	}
	if bodyDocID == "" {
		bodyName := firstNonEmpty(env.Subject, "(no subject)") + ".md"
		id, err := s.ingest.IngestFile(ctx, cfg.TenantID, cfg.CreatedBy, "",
			cfg.TargetWorkspaceID, cfg.TargetFolderID, bodyName, "text/markdown",
			[]byte(formatBody(env)), baseMeta)
		if err != nil {
			return fmt.Errorf("body: %w", err)
		}
		if err := s.stampPart(ctx, cfg, msgID, 0, id); err != nil {
			return fmt.Errorf("stamp body: %w", err)
		}
		bodyDocID = id
	}
	for i, att := range env.Attachments {
		n := i + 1
		if attachDone[n] {
			continue
		}
		ct := att.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		name := att.Filename
		if strings.TrimSpace(name) == "" {
			name = fmt.Sprintf("attachment-%d", n)
		}
		childMeta := map[string]any{
			"email.parent_doc_id": bodyDocID,
			"email.from":          env.From,
			"email.filename":      name,
			"email.part_index":    n,
		}
		id, err := s.ingest.IngestFile(ctx, cfg.TenantID, cfg.CreatedBy, "",
			cfg.TargetWorkspaceID, cfg.TargetFolderID, name, ct, att.Bytes, childMeta)
		if err != nil {
			return fmt.Errorf("attachment %d (%s): %w", n, name, err)
		}
		if err := s.stampPart(ctx, cfg, msgID, n, id); err != nil {
			return fmt.Errorf("stamp attachment %d: %w", n, err)
		}
	}
	return nil
}

// loadParts reads which parts of a message are already filed.
func (s *Service) loadParts(ctx context.Context, cfg *Config, msgID string) (string, map[int]bool, error) {
	var bodyDocID string
	var attachJSON []byte
	err := s.withTenant(ctx, cfg.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT COALESCE(document_id::text, ''),
			       COALESCE(array_to_json(attachment_document_ids)::text, '[]')
			  FROM email_messages WHERE tenant_id = $1 AND id = $2`,
			cfg.TenantID, msgID).Scan(&bodyDocID, &attachJSON)
	})
	if err != nil {
		return "", nil, err
	}
	var ids []*string
	if err := json.Unmarshal(attachJSON, &ids); err != nil {
		return "", nil, err
	}
	done := map[int]bool{}
	for i, id := range ids {
		if id != nil && *id != "" {
			done[i+1] = true
		}
	}
	return bodyDocID, done, nil
}

// stampPart records one filed part: part 0 → document_id, part n →
// attachment_document_ids[n] (Postgres arrays are 1-based; earlier holes
// stay NULL until their part is filed).
func (s *Service) stampPart(ctx context.Context, cfg *Config, msgID string, part int, docID string) error {
	return s.withTenant(ctx, cfg.TenantID, func(tx pgx.Tx) error {
		if part == 0 {
			_, err := tx.Exec(ctx, `UPDATE email_messages SET document_id = $3::uuid WHERE tenant_id = $1 AND id = $2`,
				cfg.TenantID, msgID, docID)
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE email_messages SET attachment_document_ids[$3] = $4::uuid WHERE tenant_id = $1 AND id = $2`,
			cfg.TenantID, msgID, part, docID)
		return err
	})
}

func (s *Service) recordError(ctx context.Context, cfg *Config, msg string) {
	s.log.Warn().Str("config_id", cfg.ID).Str("source", string(cfg.Source)).Str("err", msg).Msg("email poll failed")
	if err := s.withTenant(ctx, cfg.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE email_ingestion_configs
			    SET last_run_at = now(), last_error = $3, updated_at = now()
			  WHERE tenant_id = $1 AND id = $2`,
			cfg.TenantID, cfg.ID, msg)
		return err
	}); err != nil {
		s.log.Error().Err(err).Str("config_id", cfg.ID).Msg("email: record error stamp failed")
	}
}

func (s *Service) recordSuccess(ctx context.Context, cfg *Config, ingested int) {
	if err := s.withTenant(ctx, cfg.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE email_ingestion_configs
			    SET last_run_at = now(), last_success_at = now(),
			        last_error = NULL,
			        messages_ingested = messages_ingested + $3,
			        updated_at = now()
			  WHERE tenant_id = $1 AND id = $2`,
			cfg.TenantID, cfg.ID, ingested)
		return err
	}); err != nil {
		s.log.Error().Err(err).Str("config_id", cfg.ID).Msg("email: record success stamp failed")
	}
}

// RunOnce is the manual "poll now" path triggered by the admin UI.
func (s *Service) RunOnce(ctx context.Context, tenantID, configID string) (int, error) {
	cfg, err := s.getConfig(ctx, tenantID, configID)
	if err != nil || cfg == nil {
		return 0, err
	}
	poller, ok := s.pollers[cfg.Source]
	if !ok {
		return 0, fmt.Errorf("unsupported source: %s", cfg.Source)
	}
	envelopes, err := poller.Poll(ctx, cfg)
	if err != nil {
		s.recordError(ctx, cfg, err.Error())
		return 0, err
	}
	ingested, err := s.persistEnvelopes(ctx, cfg, envelopes)
	if err != nil {
		s.recordError(ctx, cfg, err.Error())
		return 0, err
	}
	s.recordSuccess(ctx, cfg, ingested)
	return ingested, nil
}

// ---- CRUD ----------------------------------------------------------------

// CreateConfig persists a fresh ingestion config. IMAP rows get their
// password encrypted-at-rest; OAuth rows reuse the existing
// connector_configs row referenced by `oauth_provider`.
func (s *Service) CreateConfig(ctx context.Context, tenantID, actorID string, in CreateConfigInput) (*Config, error) {
	if in.Label == "" {
		return nil, errors.New("label required")
	}
	if in.PollIntervalSeconds < 60 {
		in.PollIntervalSeconds = 300
	}
	if in.IMAPUseTLS == false && in.Source == SourceIMAP && in.IMAPPort == 0 {
		in.IMAPPort = 143
	}
	if in.Source == SourceIMAP && in.IMAPPort == 0 {
		in.IMAPPort = 993
	}
	if in.Source == SourceIMAP {
		if err := validateIMAPEndpoint(in.IMAPHost, in.IMAPPort, s.allowPrivateIMAP); err != nil {
			return nil, err
		}
	}

	var encryptedPwd []byte
	if in.Source == SourceIMAP && in.IMAPPassword != "" {
		b, err := encryptPassword(in.IMAPPassword)
		if err != nil {
			return nil, fmt.Errorf("encrypt imap password: %w", err)
		}
		encryptedPwd = b
	}

	id := newUUID()
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		var actor any
		if actorID != "" {
			actor = actorID
		}
		var workspaceID, folderID any
		if in.TargetWorkspaceID != "" {
			workspaceID = in.TargetWorkspaceID
		}
		if in.TargetFolderID != "" {
			folderID = in.TargetFolderID
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO email_ingestion_configs
			    (tenant_id, id, source, label, active, oauth_provider,
			     imap_host, imap_port, imap_use_tls, imap_username,
			     imap_password_encrypted,
			     target_workspace_id, target_folder_id,
			     poll_interval_seconds, created_by)
			VALUES ($1, $2, $3, $4, TRUE, NULLIF($5, ''),
			        NULLIF($6, ''), NULLIF($7, 0), $8, NULLIF($9, ''),
			        $10, $11, $12, $13, $14)`,
			tenantID, id, string(in.Source), in.Label,
			in.OAuthProvider,
			in.IMAPHost, in.IMAPPort, in.IMAPUseTLS, in.IMAPUsername,
			encryptedPwd,
			workspaceID, folderID,
			in.PollIntervalSeconds, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.getConfig(ctx, tenantID, id)
}

// ListConfigs returns all configs for a tenant, secrets stripped.
func (s *Service) ListConfigs(ctx context.Context, tenantID string) ([]*Config, error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	var out []*Config
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, source, label, active,
			       COALESCE(oauth_provider, ''),
			       COALESCE(imap_host, ''), COALESCE(imap_port, 0), imap_use_tls,
			       COALESCE(imap_username, ''),
			       COALESCE(target_workspace_id::text, ''),
			       COALESCE(target_folder_id::text, ''),
			       poll_interval_seconds, last_run_at, last_success_at,
			       COALESCE(last_error, ''), messages_ingested,
			       COALESCE(created_by::text, ''), created_at
			  FROM email_ingestion_configs
			 WHERE tenant_id = $1
			 ORDER BY created_at DESC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c := &Config{TenantID: tenantID}
			// created_by is column 17 of 18 — omitting its destination
			// made every List() fail with "number of field descriptions
			// must equal number of destinations, got 18 and 17", so the
			// Email ingestion tab 500'd on load and after every create.
			// The Get/due-poll queries alongside this one always scanned
			// it; only this copy drifted.
			if err := rows.Scan(&c.ID, &c.Source, &c.Label, &c.Active,
				&c.OAuthProvider, &c.IMAPHost, &c.IMAPPort, &c.IMAPUseTLS,
				&c.IMAPUsername, &c.TargetWorkspaceID, &c.TargetFolderID,
				&c.PollIntervalSeconds, &c.LastRunAt, &c.LastSuccessAt,
				&c.LastError, &c.MessagesIngested, &c.CreatedBy, &c.CreatedAt); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// PatchConfig updates a subset of fields. Returns the updated config.
func (s *Service) PatchConfig(ctx context.Context, tenantID, configID string, in PatchConfigInput) (*Config, error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE email_ingestion_configs SET
			    active                = COALESCE($3, active),
			    label                 = COALESCE($4, label),
			    target_workspace_id   = COALESCE($5::uuid, target_workspace_id),
			    target_folder_id      = COALESCE($6::uuid, target_folder_id),
			    poll_interval_seconds = COALESCE($7, poll_interval_seconds),
			    updated_at            = now()
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID, configID,
			in.Active, in.Label, in.TargetWorkspaceID, in.TargetFolderID,
			in.PollIntervalSeconds)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.getConfig(ctx, tenantID, configID)
}

// DeleteConfig tombstones a config (active=false). We don't physical-delete
// because email_messages rows are FK'd here and customers occasionally
// want to re-enable a config without losing the prior ingestion history.
func (s *Service) DeleteConfig(ctx context.Context, tenantID, configID string) error {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE email_ingestion_configs SET active = FALSE, updated_at = now()
			  WHERE tenant_id = $1 AND id = $2`, tenantID, configID)
		return err
	})
}

func (s *Service) getConfig(ctx context.Context, tenantID, id string) (*Config, error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	c := &Config{TenantID: tenantID}
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id::text, source, label, active,
			       COALESCE(oauth_provider, ''),
			       COALESCE(imap_host, ''), COALESCE(imap_port, 0), imap_use_tls,
			       COALESCE(imap_username, ''),
			       COALESCE(target_workspace_id::text, ''),
			       COALESCE(target_folder_id::text, ''),
			       poll_interval_seconds, last_run_at, last_success_at,
			       COALESCE(last_error, ''), messages_ingested,
			       COALESCE(created_by::text, ''), created_at
			  FROM email_ingestion_configs
			 WHERE tenant_id = $1 AND id = $2`, tenantID, id,
		).Scan(&c.ID, &c.Source, &c.Label, &c.Active,
			&c.OAuthProvider, &c.IMAPHost, &c.IMAPPort, &c.IMAPUseTLS,
			&c.IMAPUsername, &c.TargetWorkspaceID, &c.TargetFolderID,
			&c.PollIntervalSeconds, &c.LastRunAt, &c.LastSuccessAt,
			&c.LastError, &c.MessagesIngested, &c.CreatedBy, &c.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// GetStats returns aggregate counts + scheduling info for a single config.
func (s *Service) GetStats(ctx context.Context, tenantID, configID string) (*Stats, error) {
	cfg, err := s.getConfig(ctx, tenantID, configID)
	if err != nil || cfg == nil {
		return nil, err
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	stats := &Stats{
		ConfigID:      configID,
		LastRunAt:     cfg.LastRunAt,
		LastSuccessAt: cfg.LastSuccessAt,
		LastError:     cfg.LastError,
	}
	if cfg.LastRunAt != nil {
		next := cfg.LastRunAt.Add(time.Duration(cfg.PollIntervalSeconds) * time.Second)
		stats.NextRunAt = &next
	}
	return stats, database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT
			    COUNT(*) FILTER (WHERE TRUE)                              AS total,
			    COUNT(*) FILTER (WHERE ingest_status = 'pending')         AS pending,
			    COUNT(*) FILTER (WHERE ingest_status = 'failed')          AS failed
			  FROM email_messages
			 WHERE tenant_id = $1 AND config_id = $2`,
			tenantID, configID,
		).Scan(&stats.MessagesTotal, &stats.MessagesPending, &stats.MessagesFailed)
	})
}

// ---- helpers --------------------------------------------------------------

func emailIngestedPayload(cfg *Config, env *Envelope, msgID string) []byte {
	envelope := map[string]any{
		"type": "dms.email.ingested.v1",
		"data": map[string]any{
			"tenant_id":           cfg.TenantID,
			"config_id":           cfg.ID,
			"message_id":          msgID,
			"target_workspace_id": cfg.TargetWorkspaceID,
			"target_folder_id":    cfg.TargetFolderID,
			"subject":             env.Subject,
			"from":                env.From,
			"to":                  env.To,
			"date":                env.Date.Format(time.RFC3339),
			"thread_id":           env.ThreadID,
			"body_text":           env.BodyText,
			"body_html":           env.BodyHTML,
			"attachment_count":    len(env.Attachments),
		},
	}
	b, _ := json.Marshal(envelope)
	return b
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
