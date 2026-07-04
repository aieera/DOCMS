// Package service is the document service's business-logic layer. Every
// public method follows the same template:
//
//  1. extract tenant + user from context
//  2. validate input
//  3. call PolicyService for an allow/deny decision (fail closed)
//  4. run the write + outbox insert inside a single pgx.Tx via WithTenantTx
//  5. return a domain object to the handler
//
// The handler layer is responsible for wire-level (proto) translation.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/storage"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
)

// PermissionChecker is the subset of PolicyServiceClient the service uses.
// Declaring it as a local interface lets tests inject a mock without spinning
// up a real gRPC server.
type PermissionChecker interface {
	CheckPermission(ctx context.Context, in *sedocv1.CheckPermissionRequest, opts ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error)
	BatchCheckPermission(ctx context.Context, in *sedocv1.BatchCheckPermissionRequest, opts ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error)
}

// DocumentPermissions is the 5-axis permission summary returned alongside a
// document when the caller asks for it (only GetDocument today).
type DocumentPermissions struct {
	CanView, CanEdit, CanDelete, CanShare, CanAdmin bool
}

// HoldsChecker is the narrow interface DocumentService needs from
// the compliance package to enforce legal-hold binding checks on
// mutating paths. Using a local interface avoids a hard dependency on
// compliance (and the import cycle that would follow).
type HoldsChecker interface {
	AnyActiveHoldFor(ctx context.Context, tenantID, documentID uuid.UUID) (bool, error)
}

// RecordsChecker is the narrow interface DocumentService needs from the
// records package to enforce record immutability on mutating paths: a
// document declared as a record (not yet disposed) cannot be edited, moved,
// re-versioned, or deleted except via disposition. Local interface avoids an
// import cycle (records imports model; service would otherwise import records).
type RecordsChecker interface {
	IsDeclaredRecord(ctx context.Context, tenantID, documentID uuid.UUID) (bool, error)
}

// DocumentService orchestrates repositories + PolicyService.
type DocumentService struct {
	pool     *pgxpool.Pool
	repos    *repository.Repositories
	policy   PermissionChecker
	holds    HoldsChecker
	records  RecordsChecker
	log      zerolog.Logger
	localKEK []byte // 32 bytes; nil disables tenant-secret encrypt/decrypt paths
	// env mirrors pkg/config.Config.Environment ("dev" | "staging" | "prod").
	// Read by entity-name validation to decide between warn (non-prod)
	// and hard-reject (prod). Empty string = treat as non-prod (lenient).
	env string
	// s3 is set by main.go for the admin Trash purge path. Nil means
	// PurgeDocument cannot proceed (admin Trash falls back to a 503).
	s3 *storage.S3Client
	// matchThreshold is the WS3 routing confidence gate (default 0.85 when
	// unset). At or above it a staged ingestion item auto-commits; below it
	// the item goes to the review queue. Set from SEDOC_INGEST_MATCH_THRESHOLD.
	matchThreshold float64
	// folderBuckets is the WS6 customer-folder sharding scheme used by
	// PlaceCustomerFolder so no single parent holds tens of thousands of direct
	// children. Zero value falls back to the hash/2 default. Set from
	// SEDOC_FOLDER_BUCKET_MODE / SEDOC_FOLDER_BUCKET_PREFIX_LEN.
	folderBuckets FolderBucketScheme
}

// SetFolderBucketScheme installs the WS6 customer-folder sharding scheme.
func (s *DocumentService) SetFolderBucketScheme(scheme FolderBucketScheme) {
	s.folderBuckets = scheme
}

// SetMatchThreshold overrides the WS3 routing confidence gate. Values ≤ 0 are
// ignored so the 0.85 default stands.
func (s *DocumentService) SetMatchThreshold(t float64) {
	if t > 0 {
		s.matchThreshold = t
	}
}

// MatchThreshold returns the effective WS3 routing confidence gate.
func (s *DocumentService) MatchThreshold() float64 {
	if s.matchThreshold > 0 {
		return s.matchThreshold
	}
	return 0.85
}

// SetS3Client wires the S3/MinIO client used by the admin Trash
// purge path so it can delete blob bytes alongside the DB rows.
func (s *DocumentService) SetS3Client(c *storage.S3Client) { s.s3 = c }

// SetRecordsChecker installs the records immutability gate. When set, mutating
// document paths refuse changes to a declared (non-disposed) record with
// ErrRecordDeclared. Nil disables the check (used by tests / pre-records boot).
func (s *DocumentService) SetRecordsChecker(c RecordsChecker) { s.records = c }

// blockedByRecord returns ErrRecordDeclared when documentID is a declared
// record (immutable until disposition). No-op when the checker is unset.
func (s *DocumentService) blockedByRecord(ctx context.Context, tenantID, documentID uuid.UUID) error {
	if s.records == nil {
		return nil
	}
	declared, err := s.records.IsDeclaredRecord(ctx, tenantID, documentID)
	if err != nil {
		return fmt.Errorf("record check: %w", err)
	}
	if declared {
		return vdmserr.ErrRecordDeclared
	}
	return nil
}

// blockedByWORM returns ErrWORMLocked when the document's blob is under an
// unexpired S3 object-lock retention (app-level guard mirroring the S3-enforced
// lock). Cheap targeted read; WORM is an admin-rare designation.
func (s *DocumentService) blockedByWORM(ctx context.Context, tenantID, documentID uuid.UUID) error {
	var until *time.Time
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT worm_retain_until FROM documents WHERE tenant_id=$1 AND id=$2`,
			tenantID, documentID).Scan(&until)
	})
	if err != nil {
		return fmt.Errorf("worm check: %w", err)
	}
	if until != nil && until.After(time.Now()) {
		return vdmserr.ErrWORMLocked
	}
	return nil
}

// SetLocalKEK installs the AES-256 key used to encrypt/decrypt small
// per-tenant secrets stored in the document DB (currently the LLM API
// key in ner_config). Loaded from SEDOC_LOCAL_KEK in main.go; nil
// or wrong size means encrypt-requiring endpoints fail with a clear
// 500 instead of silently storing plaintext.
func (s *DocumentService) SetLocalKEK(kek []byte) { s.localKEK = kek }

// SetEnvironment plumbs cfg.Environment into the service so name
// validators (workspaces, tags, folders) can decide whether to
// reject too-short names (prod) or warn-only (dev/staging).
func (s *DocumentService) SetEnvironment(env string) { s.env = env }

// SetHoldsChecker wires a hold-binding checker into DocumentService.
// Optional: when nil, delete/dispose paths fall back to the
// lifecycle_state-based check only (Wave 8.2 keeps both paths so a
// deployment without the compliance package still enforces via state).
func (s *DocumentService) SetHoldsChecker(h HoldsChecker) { s.holds = h }

// New constructs a DocumentService. Callers wire repositories and a Policy
// gRPC client in cmd/server/main.go.
func New(pool *pgxpool.Pool, repos *repository.Repositories, policy PermissionChecker, log zerolog.Logger) *DocumentService {
	return &DocumentService{
		pool:   pool,
		repos:  repos,
		policy: policy,
		log:    log,
	}
}

// ---- Input types (plain structs; handler maps from proto) -----------------

type CreateDocumentInput struct {
	WorkspaceID    uuid.UUID
	FolderID       uuid.UUID
	Title          string
	Description    string
	RegionPin      string
	CustomMetadata map[string]any
	Tags           []string
	UpdatedBy      uuid.UUID
	// ExternalID is an optional caller-owned business key. Empty = none.
	// Tenant-unique when set; a collision surfaces as ErrAlreadyExists.
	ExternalID string
	// DocType is "file" (default), "note", or "wiki". Notes/wikis are
	// normal documents whose content is a collaborative markdown version,
	// so they inherit versioning/ACL/search/audit.
	DocType string
}

// CreateNoteInput creates a note/wiki document. Content is added later as a
// markdown version through the normal version path; the document exists
// immediately so the collaborative editor can open on it.
type CreateNoteInput struct {
	WorkspaceID uuid.UUID
	FolderID    uuid.UUID
	Title       string
	// DocType must be model.DocTypeNote or model.DocTypeWiki; empty = note.
	DocType string
}

type UpdateDocumentInput struct {
	DocumentID     uuid.UUID
	Title          *string
	Description    *string
	CustomMetadata map[string]any // nil = don't touch
	Tags           []string       // nil = don't touch; use ClearTags to empty
	ClearTags      bool
	UpdatedBy      uuid.UUID
}

type MoveDocumentInput struct {
	DocumentID        uuid.UUID
	TargetFolderID    uuid.UUID
	TargetWorkspaceID *uuid.UUID // nil = stay
	UpdatedBy         uuid.UUID
}

// CopyDocumentInput names a source doc + a destination folder. Same
// shape as Move except the source row stays intact. The new row gets
// a fresh UUID and uses the caller as created_by, but content_blob_id
// + sha256 + size are copied verbatim — copy is shallow at the
// version level (only the current version is brought over). Versions,
// share-links, comments, annotations are NOT copied.
type CopyDocumentInput struct {
	DocumentID        uuid.UUID
	TargetFolderID    uuid.UUID
	TargetWorkspaceID *uuid.UUID // nil = same workspace as source folder
	CopiedBy          uuid.UUID
}

type CreateFolderInput struct {
	WorkspaceID    uuid.UUID
	Name           string
	ParentFolderID *uuid.UUID
	// Visibility defaults to FolderShared when empty. Private folders
	// auto-set OwnerID = caller (no separate field — owner is always
	// the creator at creation time; can be transferred via PATCH).
	Visibility model.FolderVisibility
}

type UpdateFolderInput struct {
	FolderID          uuid.UUID
	Name              *string
	NewParentFolderID *uuid.UUID
}

// SetFolderVisibilityInput names the folder + the desired new
// visibility. Owner is implicit: promoting shared → private sets
// owner = caller; demoting private → shared clears the owner.
// A no-op (current visibility == requested) returns the existing
// folder unchanged.
type SetFolderVisibilityInput struct {
	FolderID   uuid.UUID
	Visibility model.FolderVisibility
}

// AddFolderGrantInput is the body for POST .../grants. The owner /
// admin authorisation gate lives in the service layer, not the
// input shape — the handler just parses the request body and the
// service decides whether the caller may grant.
type AddFolderGrantInput struct {
	FolderID    uuid.UUID
	GranteeType string // 'user' | 'group'
	GranteeID   uuid.UUID
}

type CreateVersionInput struct {
	DocumentID    uuid.UUID
	ContentBlobID uuid.UUID
	SizeBytes     int64
	MimeType      string
	SHA256Hash    string
	CreatedByName string
	ChangeSummary string
	// BaseVersionID is the version the caller based this edit on (optimistic
	// concurrency, §sync). When non-nil and it no longer equals the document's
	// current_version_id, CreateVersion returns vdmserr.Conflict (409). Nil =
	// skip the check.
	BaseVersionID *uuid.UUID
}

type UpdateLifecycleInput struct {
	DocumentID          uuid.UUID
	Action              model.LifecycleAction
	Reason              string
	HoldName            string
	HoldMatterReference string
}

type CreateShareLinkInput struct {
	DocumentID     uuid.UUID
	Password       string
	ExpiresInHours int
	MaxViews       int
	Permissions    []string
}

type AccessShareLinkInput struct {
	Token    string
	Password string
}

type AccessShareLinkResult struct {
	Document         *model.Document
	DownloadURL      string
	PasswordRequired bool
}

type CreateTagInput struct {
	Name  string
	Color string
}

type BatchUpdateMetadataInput struct {
	DocumentIDs     []uuid.UUID
	MetadataUpdates map[string]any
}

type BatchUpdateMetadataResult struct {
	UpdatedCount      int
	FailedDocumentIDs []uuid.UUID
	FailureReasons    []string
}

// ---- Validation helpers ---------------------------------------------------

var (
	// folderNameRE — display name for folders. Previously
	// `[A-Za-z0-9 _-]` only, which rejected legitimate enterprise
	// names like "Q3 2025 (Final)" or "ABC Corp. & Co." Relaxed to a
	// denylist: forbid path-separator + filesystem-reserved + control
	// chars; allow everything else including Unicode + common
	// punctuation. The ltree path label is generated separately by
	// ltreeLabel() which slugs to [A-Za-z0-9_] regardless, so the
	// display value never escapes into ltree semantics.
	folderNameRE   = regexp.MustCompile(`^[^/\\:*?"<>|\x00-\x1F]{1,255}$`)
	tagNameRE      = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,64}$`)
	hexColorRE     = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	allowedRegions = map[string]struct{}{
		"us-east-1":      {},
		"eu-west-1":      {},
		"me-south-1":     {},
		"ap-southeast-1": {},
	}
	validShareActions = map[string]struct{}{"view": {}, "download": {}}
	maxFolderDepth    = 20
)

func validateTitle(title string) error {
	if n := len(title); n == 0 || n > 255 {
		return vdmserr.Validation("title", "length must be 1..255")
	}
	return nil
}

func validateDescription(d string) error {
	if len(d) > 4096 {
		return vdmserr.Validation("description", "max 4096 characters")
	}
	return nil
}

func validateFolderName(name string) error {
	if !folderNameRE.MatchString(name) {
		return vdmserr.Validation("name", `must be 1..255 chars; cannot contain / \ : * ? " < > | or control chars`)
	}
	return nil
}

func validateRegion(r string) error {
	if r == "" {
		return nil // optional; service fills a default
	}
	if _, ok := allowedRegions[r]; !ok {
		return vdmserr.Validation("region_pin", "unsupported region")
	}
	return nil
}

func validateTagName(name string) error {
	if !tagNameRE.MatchString(name) {
		return vdmserr.Validation("name", "must be 1..64 chars [A-Za-z0-9_-]")
	}
	return nil
}

func validateColor(c string) error {
	if c == "" {
		return nil
	}
	if !hexColorRE.MatchString(c) {
		return vdmserr.Validation("color", "must be #RRGGBB hex")
	}
	return nil
}

func validateSharePermissions(perms []string) error {
	if len(perms) == 0 {
		return vdmserr.Validation("permissions", "at least one permission required")
	}
	for _, p := range perms {
		if _, ok := validShareActions[p]; !ok {
			return vdmserr.Validation("permissions", "unsupported permission: "+p)
		}
	}
	return nil
}

// ---- Permission helpers ---------------------------------------------------

// checkPermission calls PolicyService. On transport error it logs and DENIES
// (fail closed). The ABAC context is encoded as google.protobuf.Struct.
//
// Cross-service identity propagation (CLAUDE.md): the policy service's
// TenantInterceptor returns Unauthenticated unless every outbound RPC
// carries x-tenant-id (and x-user-id / x-user-role for OPA Rule 5/6).
// Without this block every CheckPermission was Unauthenticated → the
// fail-closed branch below turned that into a generic 403.
func (s *DocumentService) checkPermission(ctx context.Context, userID uuid.UUID, action, resourceType string, resourceID uuid.UUID, extra map[string]any) (bool, error) {
	ok, _, err := s.checkPermissionDetailed(ctx, userID, action, resourceType, resourceID, extra)
	return ok, err
}

// checkPermissionDetailed is checkPermission plus the policy's reason string.
// The reason is generic ("policy denied") for ACL/lifecycle denials and a
// specific, user-facing "blocked: ..." message for classification/clearance
// blocks (§8) — the read path surfaces the latter as an explainable HTTP 403.
func (s *DocumentService) checkPermissionDetailed(ctx context.Context, userID uuid.UUID, action, resourceType string, resourceID uuid.UUID, extra map[string]any) (bool, string, error) {
	ctxStruct, err := structpb.NewStruct(stringifyMap(extra))
	if err != nil {
		return false, "", fmt.Errorf("build context struct: %w", err)
	}
	pairs := []string{"x-user-id", userID.String()}
	if tid, terr := auth.GetTenantID(ctx); terr == nil && tid != uuid.Nil {
		pairs = append(pairs, "x-tenant-id", tid.String())
	}
	if name := auth.GetUserName(ctx); name != "" {
		// Forward the caller's email so the downstream service's
		// TenantInterceptor can stamp it onto outbox rows; without
		// this, every cross-service write produced an audit_events
		// row with actor_name="" (BUG-15).
		pairs = append(pairs, "x-user-name", name)
	}
	if role := auth.GetUserRole(ctx); role != "" {
		pairs = append(pairs, "x-user-role", role)
		// Forward role into the OPA context too so Rule 6 (owner/admin)
		// fires. The interceptor doesn't know the input.context shape.
		if extra == nil {
			extra = map[string]any{}
		}
		if _, ok := extra["user_role"]; !ok {
			extra["user_role"] = role
		}
		// Re-marshal the struct now that user_role landed.
		ctxStruct, err = structpb.NewStruct(stringifyMap(extra))
		if err != nil {
			return false, "", fmt.Errorf("build context struct: %w", err)
		}
	}
	ctx = metadata.AppendToOutgoingContext(ctx, pairs...)
	resp, err := s.policy.CheckPermission(ctx, &sedocv1.CheckPermissionRequest{
		SubjectType:  "user",
		SubjectId:    userID.String(),
		Action:       action,
		ResourceType: resourceType,
		ResourceId:   resourceID.String(),
		Context:      ctxStruct,
	})
	if err != nil {
		s.log.Error().Err(err).Str("action", action).Str("resource", resourceType).
			Msg("policy service unavailable; denying request (fail-closed)")
		return false, "", nil
	}
	return resp.GetAllowed(), resp.GetReason(), nil
}

// requirePermission short-circuits with ErrForbidden if the check denies.
func (s *DocumentService) requirePermission(ctx context.Context, userID uuid.UUID, action, resourceType string, resourceID uuid.UUID, extra map[string]any) error {
	ok, err := s.checkPermission(ctx, userID, action, resourceType, resourceID, extra)
	if err != nil {
		return err
	}
	if !ok {
		return vdmserr.ErrForbidden
	}
	return nil
}

// requireDocPermission loads the document, then runs requirePermission
// with the workspace + lifecycle + region context that OPA's document
// rules need. Without this context the policy can't evaluate workspace-
// membership and denies — the bug intelligence-feature endpoints hit
// when they call requirePermission with a nil extra map.
func (s *DocumentService) requireDocPermission(
	ctx context.Context,
	tenantID, userID, docID uuid.UUID,
	action string,
) (*model.Document, error) {
	var doc *model.Document
	if err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		d, err := s.repos.Documents.GetByID(ctx, tx, tenantID, docID)
		if err != nil {
			return err
		}
		if d.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		doc = d
		return nil
	}); err != nil {
		return nil, err
	}
	extra := map[string]any{
		"workspace_id":     doc.WorkspaceID.String(),
		"lifecycle_state":  string(doc.LifecycleState),
		"region_pin":       doc.RegionPin,
		"classification":   doc.DocumentClass,
		"under_legal_hold": doc.LifecycleState == model.StateLegalHold,
	}
	// §8: merge the classification/clearance gate context (no-op when the
	// tenant hasn't enabled gating). Actions map onto the same vocabulary the
	// rules use (view/edit/share/delete), so this gates every action, not just
	// reads.
	gate, err := s.loadClassGate(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	for k, v := range gate.contextFor(doc, action) {
		extra[k] = v
	}
	allowed, reason, err := s.checkPermissionDetailed(ctx, userID, action, "document", docID, extra)
	if err != nil {
		return nil, err
	}
	if !allowed {
		if blocked := s.classificationDenial(ctx, tenantID, userID, doc, action, reason); blocked != nil {
			return nil, blocked
		}
		return nil, vdmserr.ErrForbidden
	}
	return doc, nil
}

// classGate bundles the per-request inputs the classification gate needs: the
// tenant config, its rules, and the caller's clearance. Loaded once per request
// so batch paths don't re-query per document.
type classGate struct {
	cfg       model.ClassificationGateConfig
	rules     []model.ClassificationRule
	clearance string
}

// loadClassGate reads the gate config + rules + caller clearance in one tenant
// tx. When gating is disabled for the tenant, contextFor returns nil and the
// read path is byte-for-byte unchanged.
func (s *DocumentService) loadClassGate(ctx context.Context, tenantID, userID uuid.UUID) (*classGate, error) {
	g := &classGate{}
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cfg, err := s.repos.Classification.GetGateConfig(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		g.cfg = cfg
		if !cfg.Enabled {
			return nil
		}
		rules, err := s.repos.Classification.ListRules(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		g.rules = rules
		if userID != uuid.Nil {
			cl, err := s.repos.Classification.GetUserClearance(ctx, tx, tenantID, userID)
			if err != nil {
				return err
			}
			g.clearance = cl
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return g, nil
}

// contextFor produces the ABAC context fields the OPA classification gate reads
// for (doc, action). Returns nil when gating is disabled. security_classification
// carries the *effective* level (PHI/PII floor applied) so the reason names it.
func (g *classGate) contextFor(doc *model.Document, action string) map[string]any {
	if g == nil || !g.cfg.Enabled {
		return nil
	}
	eff := model.EffectiveClassification(g.cfg, doc.SecurityClassification, doc.HasPHI, doc.HasPII)
	req := model.RequiredClearance(g.rules, eff, doc.HasPHI, action)
	return map[string]any{
		"classification_gate":     "on",
		"security_classification": eff,
		"has_phi":                 boolStr(doc.HasPHI),
		"has_pii":                 boolStr(doc.HasPII),
		"user_clearance":          g.clearance,
		"required_clearance":      req,
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// enforceClassificationView applies the classification gate for a "view" after
// the ACL check has already granted visibility. Returns nil (allowed / gating
// disabled), an explainable *vdmserr.Error (classification block, HTTP 403), or
// ErrForbidden. Callers must only invoke this once ACL visibility is confirmed,
// so a clearance block is never revealed to someone who couldn't see the doc.
func (s *DocumentService) enforceClassificationView(ctx context.Context, tenantID, userID uuid.UUID, doc *model.Document) error {
	gate, err := s.loadClassGate(ctx, tenantID, userID)
	if err != nil {
		return err
	}
	gc := gate.contextFor(doc, "view")
	if gc == nil {
		return nil // gating disabled
	}
	gc["workspace_id"] = doc.WorkspaceID.String()
	allowed, reason, err := s.checkPermissionDetailed(ctx, userID, "view", "document", doc.ID, gc)
	if err != nil {
		return err
	}
	if allowed {
		return nil
	}
	if blocked := s.classificationDenial(ctx, tenantID, userID, doc, "view", reason); blocked != nil {
		return blocked
	}
	return vdmserr.ErrForbidden
}

// classificationDenial turns a policy "blocked: ..." reason into an explainable
// 403 and audits the decision. Returns nil when the denial wasn't a
// classification block (the caller then applies its normal deny behaviour).
func (s *DocumentService) classificationDenial(ctx context.Context, tenantID, userID uuid.UUID, doc *model.Document, action, reason string) error {
	if !strings.HasPrefix(reason, "blocked:") {
		return nil
	}
	s.auditClassificationDeny(ctx, tenantID, userID, doc, action, reason)
	return &vdmserr.Error{Kind: vdmserr.KindForbidden, Code: "CLASSIFICATION_BLOCKED", Message: reason}
}

type policyDeniedPayload struct {
	TenantID               string `json:"tenant_id"`
	DocumentID             string `json:"document_id"`
	UserID                 string `json:"user_id"`
	Action                 string `json:"action"`
	Reason                 string `json:"reason"`
	SecurityClassification string `json:"security_classification"`
	HasPHI                 bool   `json:"has_phi"`
	HasPII                 bool   `json:"has_pii"`
	DeniedAt               string `json:"denied_at"`
}

// auditClassificationDeny records a classification/clearance block to the audit
// trail via the transactional outbox (dms.policy.denied.v1). Best-effort: a
// failure to audit must not change the (already-decided) deny outcome, so the
// error is logged, not returned.
func (s *DocumentService) auditClassificationDeny(ctx context.Context, tenantID, userID uuid.UUID, doc *model.Document, action, reason string) {
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		evt, oErr := model.NewOutboxEvent(tenantID, "dms.policy.denied.v1", "document", doc.ID, policyDeniedPayload{
			TenantID:               tenantID.String(),
			DocumentID:             doc.ID.String(),
			UserID:                 userID.String(),
			Action:                 action,
			Reason:                 reason,
			SecurityClassification: doc.SecurityClassification,
			HasPHI:                 doc.HasPHI,
			HasPII:                 doc.HasPII,
			DeniedAt:               time.Now().UTC().Format(time.RFC3339Nano),
		})
		if oErr != nil {
			return oErr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	if err != nil {
		s.log.Error().Err(err).Str("document", doc.ID.String()).Msg("failed to audit classification denial")
	}
}

// EnsureCanViewDocument is the exported per-document view gate used by
// the download / decrypt-stream / GetDownloadURL paths (FIX-2). It
// loads the document for tenant-scope + lifecycle checks, then
// resolves the caller's effective view permission via
// summarizeDocumentPermissions and returns:
//
//   - nil               — caller may read the bytes
//   - vdmserr.ErrNotFound — document doesn't exist in this tenant
//     (or caller lacks any visibility)
//   - vdmserr.ErrForbidden — document exists and caller can see it,
//     but lacks view (e.g. revoked grant)
//
// Returning NotFound for the "cannot see it at all" case avoids
// leaking existence to enumerators. Forbidden is only returned for
// cases where existence is already public (the document service's
// own GetDocument would have returned it).
func (s *DocumentService) EnsureCanViewDocument(ctx context.Context, docID uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	var doc *model.Document
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		doc, err = s.repos.Documents.GetByID(ctx, tx, tenantID, docID)
		return err
	})
	if err != nil {
		return err
	}
	if doc == nil {
		return vdmserr.ErrNotFound
	}
	perms, err := s.summarizeDocumentPermissions(ctx, userID, docID, map[string]any{
		"workspace_id": doc.WorkspaceID.String(),
	})
	if err != nil {
		return err
	}
	if !perms.CanView {
		return vdmserr.ErrNotFound
	}
	// §8: ACL grants visibility; now apply the classification/clearance gate.
	// Runs only after CanView so a clearance block is never surfaced to a
	// caller who couldn't see the document in the first place.
	return s.enforceClassificationView(ctx, tenantID, userID, doc)
}

// summarizeDocumentPermissions issues a single BatchCheckPermission for the
// 5 canonical actions on a document. One network round-trip per GetDocument.
//
// Same outgoing-metadata story as checkPermission: policy's
// TenantInterceptor returns Unauthenticated unless tenant + user + role
// land in gRPC metadata. Batch call had no metadata wiring; every
// GetDocument logged "policy batch check unavailable" and returned a
// zero-permission summary, which the handler turned into 403.
func (s *DocumentService) summarizeDocumentPermissions(ctx context.Context, userID, docID uuid.UUID, extra map[string]any) (*DocumentPermissions, error) {
	if extra == nil {
		extra = map[string]any{}
	}
	if role := auth.GetUserRole(ctx); role != "" {
		if _, ok := extra["user_role"]; !ok {
			extra["user_role"] = role
		}
	}
	ctxStruct, err := structpb.NewStruct(stringifyMap(extra))
	if err != nil {
		return nil, fmt.Errorf("build context struct: %w", err)
	}
	actions := []string{"view", "edit", "delete", "share", "admin"}
	checks := make([]*sedocv1.CheckPermissionRequest, 0, len(actions))
	for _, a := range actions {
		checks = append(checks, &sedocv1.CheckPermissionRequest{
			SubjectType:  "user",
			SubjectId:    userID.String(),
			Action:       a,
			ResourceType: "document",
			ResourceId:   docID.String(),
			Context:      ctxStruct,
		})
	}
	pairs := []string{"x-user-id", userID.String()}
	if tid, terr := auth.GetTenantID(ctx); terr == nil && tid != uuid.Nil {
		pairs = append(pairs, "x-tenant-id", tid.String())
	}
	if name := auth.GetUserName(ctx); name != "" {
		pairs = append(pairs, "x-user-name", name)
	}
	if role := auth.GetUserRole(ctx); role != "" {
		pairs = append(pairs, "x-user-role", role)
	}
	ctx = metadata.AppendToOutgoingContext(ctx, pairs...)
	resp, err := s.policy.BatchCheckPermission(ctx, &sedocv1.BatchCheckPermissionRequest{Checks: checks})
	if err != nil {
		s.log.Warn().Err(err).Msg("policy batch check unavailable; returning zero-permission summary")
		return &DocumentPermissions{}, nil
	}
	p := &DocumentPermissions{}
	for i, r := range resp.GetResults() {
		if i >= len(actions) {
			break
		}
		switch actions[i] {
		case "view":
			p.CanView = r.GetAllowed()
		case "edit":
			p.CanEdit = r.GetAllowed()
		case "delete":
			p.CanDelete = r.GetAllowed()
		case "share":
			p.CanShare = r.GetAllowed()
		case "admin":
			p.CanAdmin = r.GetAllowed()
		}
	}
	return p, nil
}

// ---- Misc helpers ---------------------------------------------------------

// mustCaller extracts tenant + user from ctx or returns ErrUnauthorized.
func mustCaller(ctx context.Context) (tenantID, userID uuid.UUID, err error) {
	tenantID, err = auth.GetTenantID(ctx)
	if err != nil || tenantID == uuid.Nil {
		return uuid.Nil, uuid.Nil, vdmserr.ErrUnauthorized
	}
	userID, err = auth.GetUserID(ctx)
	if err != nil || userID == uuid.Nil {
		// For some flows (internal system writes) the userID might legitimately
		// be absent — callers that require a user should check for uuid.Nil.
		return tenantID, uuid.Nil, nil
	}
	return tenantID, userID, nil
}

// withTenantTx is a thin wrapper that also plumbs the logger.
func (s *DocumentService) withTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(tx pgx.Tx) error) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, fn)
}

// validateMetadataAgainstSchema checks custom_metadata against the tenant's
// JSON Schema with the two-tier semantics from the spec:
//   - shape (top-level property types) is checked on every write
//   - required-fields are ONLY checked when enforceRequired=true (currently
//     only the "transition to active" path passes true)
//
// Drafts may carry incomplete metadata; required-fields kick in at the
// in_review → active boundary so users aren't blocked from saving WIP.
//
// Routes through the full santhosh-tekuri validator (jsonschema.go); the
// legacy hand-rolled walker remains as a last-resort fallback for schemas
// that fail to compile (e.g. legacy tenants with malformed JSON Schema).
func validateMetadataAgainstSchema(schemaJSON []byte, metadata map[string]any) error {
	return validateMetadataAgainstFullSchema(schemaJSON, metadata, false)
}

// validateMetadataAgainstSchemaOpts is the underlying validator with the
// required-fields toggle. The wrapper above preserves the old call-site
// surface (always non-strict).
func validateMetadataAgainstSchemaOpts(schemaJSON []byte, metadata map[string]any, enforceRequired bool) error {
	// Cap marshaled custom_metadata size to 64 KiB
	if metadata != nil {
		marshaled, err := json.Marshal(metadata)
		if err != nil {
			return vdmserr.Validation("custom_metadata", "invalid JSON")
		}
		if len(marshaled) > 64*1024 {
			return vdmserr.Validation("custom_metadata", "exceeds 64 KiB limit")
		}
	}
	if len(schemaJSON) == 0 || string(schemaJSON) == "{}" {
		return nil
	}
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		// Corrupt schema — permissive to avoid blocking all writes.
		return nil
	}
	if enforceRequired {
		for _, k := range schema.Required {
			if _, ok := metadata[k]; !ok {
				return vdmserr.Validation("custom_metadata."+k, "required by tenant schema")
			}
		}
	}
	for k, expected := range schema.Properties {
		v, ok := metadata[k]
		if !ok {
			continue
		}
		actual := jsonTypeOf(v)
		if expected.Type != "" && actual != expected.Type {
			return vdmserr.Validation("custom_metadata."+k, "expected type "+expected.Type)
		}
	}
	return nil
}

func jsonTypeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64, float32, int, int32, int64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

func stringifyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// errInvalidInput wraps a validation error with a default field if missing.
func errInvalidInput(field, msg string) error { return vdmserr.Validation(field, msg) }
