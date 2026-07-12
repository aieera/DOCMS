// Package email implements the §12.5 email-ingestion service: per-tenant
// configs, a polling worker that fans out to Microsoft Graph / Gmail /
// IMAP, and the email→document conversion path.
//
// The OAuth halves for Microsoft and Google already live in
// services/connector/internal/providers/{microsoft,google}; this package
// glues them onto a tenant config, schedules polling, persists what was
// ingested, and dispatches each envelope into the document service via
// the existing NATS outbox (dms.email.ingested.v1 — document service
// listens and materialises the body + attachments).
package email

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
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

// Service orchestrates config CRUD + polling.
type Service struct {
	pool    *pgxpool.Pool
	nc      *nats.Conn
	ingest  *ingest.Client
	log     zerolog.Logger
	pollers map[Source]Poller
	mu      sync.Mutex
}

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
	return &Service{pool: pool, nc: nc, ingest: ingestClient, log: log, pollers: m}
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
	configs, err := s.dueConfigs(ctx)
	if err != nil {
		s.log.Error().Err(err).Msg("email: dueConfigs")
		return
	}
	for _, cfg := range configs {
		s.runConfig(ctx, cfg)
	}
}

// dueConfigs returns active configs whose last_run_at + poll_interval
// is in the past. NULL last_run_at counts as due (fresh config).
// Legitimately cross-tenant, so it uses the sanctioned enumerate-
// tenants shape (Wave A.1, issue #71): the former global scan returned
// 0 rows under prod NOBYPASSRLS and email ingestion silently stopped.
func (s *Service) dueConfigs(ctx context.Context) ([]*Config, error) {
	var out []*Config
	err := database.ForEachTenant(ctx, s.pool, func(tenantID uuid.UUID, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
		SELECT id::text, tenant_id::text, source, label, active,
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
		   AND active = TRUE
		   AND (last_run_at IS NULL
		        OR last_run_at + (poll_interval_seconds || ' seconds')::interval <= now())`, tenantID)
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
	// Phase 1 (in tx): record each NEW message as 'pending' and collect the
	// ones to materialise. We do NOT call the ingest pipeline inside the tx —
	// it makes storage gRPC + presigned-PUT round-trips that would hold a DB
	// connection open for seconds per message.
	type pending struct {
		msgID string
		env   *Envelope
	}
	var toMaterialise []pending
	var ingested int
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		for _, env := range envelopes {
			recipients, _ := json.Marshal(env.To)
			msgID := newUUID()
			tag, err := tx.Exec(ctx, `
				INSERT INTO email_messages
				    (tenant_id, id, config_id, source_message_id, thread_id,
				     subject, sender, recipients, received_at, ingest_status)
				VALUES ($1, $2, $3, $4, NULLIF($5, ''),
				        NULLIF($6, ''), NULLIF($7, ''), $8, $9, 'pending')
				ON CONFLICT (tenant_id, config_id, source_message_id) DO NOTHING`,
				cfg.TenantID, msgID, configUUID, env.SourceMessageID, env.ThreadID,
				env.Subject, env.From, recipients, nullableTime(env.Date),
			)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				continue // duplicate
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
func (s *Service) materialiseEmail(ctx context.Context, cfg *Config, msgID string, env *Envelope) {
	tenantUUID, err := uuid.Parse(cfg.TenantID)
	if err != nil {
		return
	}
	bodyDocID, attachIDs, ierr := s.ingestEmail(ctx, cfg, env)
	_ = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		if ierr != nil {
			s.log.Warn().Err(ierr).Str("config_id", cfg.ID).Msg("email: ingest failed")
			_, e := tx.Exec(ctx,
				`UPDATE email_messages SET ingest_status = 'failed', ingest_error = $3
				  WHERE tenant_id = $1 AND id = $2`,
				cfg.TenantID, msgID, ierr.Error())
			return e
		}
		_, e := tx.Exec(ctx,
			`UPDATE email_messages
			    SET ingest_status = 'materialised', document_id = $3,
			        attachment_document_ids = $4
			  WHERE tenant_id = $1 AND id = $2`,
			cfg.TenantID, msgID, bodyDocID, attachIDs)
		return e
	})
}

// ingestEmail materialises the body (rendered Markdown) + each attachment as
// real documents-with-versions via the shared ingest pipeline, running as
// the config owner (cfg.CreatedBy) over internal-service auth. Returns the
// body doc id + the attachment doc ids.
func (s *Service) ingestEmail(ctx context.Context, cfg *Config, env *Envelope) (string, []string, error) {
	if cfg.TargetWorkspaceID == "" || cfg.TargetFolderID == "" {
		return "", nil, fmt.Errorf("config %s: target workspace + folder required", cfg.ID)
	}
	baseMeta := map[string]any{
		"email.from":        env.From,
		"email.subject":     env.Subject,
		"email.received_at": env.Date.Format(time.RFC3339),
		"email.thread_id":   env.ThreadID,
		"email.source":      string(cfg.Source),
	}
	bodyName := firstNonEmpty(env.Subject, "(no subject)") + ".md"
	bodyDocID, err := s.ingest.IngestFile(ctx, cfg.TenantID, cfg.CreatedBy, "",
		cfg.TargetWorkspaceID, cfg.TargetFolderID, bodyName, "text/markdown",
		[]byte(formatBody(env)), baseMeta)
	if err != nil {
		return "", nil, fmt.Errorf("body: %w", err)
	}
	var attachIDs []string
	for _, att := range env.Attachments {
		ct := att.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		childMeta := map[string]any{
			"email.parent_doc_id": bodyDocID,
			"email.from":          env.From,
			"email.filename":      att.Filename,
		}
		id, aerr := s.ingest.IngestFile(ctx, cfg.TenantID, cfg.CreatedBy, "",
			cfg.TargetWorkspaceID, cfg.TargetFolderID, att.Filename, ct, att.Bytes, childMeta)
		if aerr != nil {
			s.log.Warn().Err(aerr).Str("filename", att.Filename).Msg("email: attachment ingest failed")
			continue
		}
		attachIDs = append(attachIDs, id)
	}
	return bodyDocID, attachIDs, nil
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
			if err := rows.Scan(&c.ID, &c.Source, &c.Label, &c.Active,
				&c.OAuthProvider, &c.IMAPHost, &c.IMAPPort, &c.IMAPUseTLS,
				&c.IMAPUsername, &c.TargetWorkspaceID, &c.TargetFolderID,
				&c.PollIntervalSeconds, &c.LastRunAt, &c.LastSuccessAt,
				&c.LastError, &c.MessagesIngested, &c.CreatedAt); err != nil {
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
