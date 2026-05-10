// Package model holds the Go-side mirrors of the GraphQL schema
// types. These types are the resolver return shape; they're built
// from the upstream gRPC responses and serialized to the wire by
// the executor.
//
// Field names are GraphQL camelCase to keep marshaling
// straightforward — the executor reads the schema field name and
// looks the value up by reflection over `json:"..."` tags.
package model

import "time"

type Document struct {
	ID                       string                 `json:"id"`
	TenantID                 string                 `json:"tenantId"`
	WorkspaceID              string                 `json:"workspaceId"`
	FolderID                 string                 `json:"folderId,omitempty"`
	Title                    string                 `json:"title"`
	Description              string                 `json:"description,omitempty"`
	LifecycleState           string                 `json:"lifecycleState"`
	RegionPin                string                 `json:"regionPin"`
	DocumentClass            string                 `json:"documentClass,omitempty"`
	ClassificationConfidence float64                `json:"classificationConfidence,omitempty"`
	Tags                     []string               `json:"tags"`
	MimeType                 string                 `json:"mimeType,omitempty"`
	TotalSizeBytes           int64                  `json:"totalSizeBytes,omitempty"`
	Sha256Hash               string                 `json:"sha256Hash,omitempty"`
	CreatedBy                string                 `json:"createdBy,omitempty"`
	CreatedAt                *time.Time             `json:"createdAt,omitempty"`
	UpdatedAt                *time.Time             `json:"updatedAt,omitempty"`
	CurrentVersionID         string                 `json:"-"`
	Permissions              *DocumentPermissions   `json:"permissions,omitempty"`
}

type DocumentPermissions struct {
	CanView   bool `json:"canView"`
	CanEdit   bool `json:"canEdit"`
	CanDelete bool `json:"canDelete"`
	CanShare  bool `json:"canShare"`
	CanAdmin  bool `json:"canAdmin"`
}

type Version struct {
	ID            string     `json:"id"`
	DocumentID    string     `json:"documentId"`
	VersionNumber int32      `json:"versionNumber"`
	ContentBlobID string     `json:"contentBlobId"`
	SizeBytes     int64      `json:"sizeBytes"`
	MimeType      string     `json:"mimeType,omitempty"`
	Sha256Hash    string     `json:"sha256Hash,omitempty"`
	CreatedBy     string     `json:"createdBy,omitempty"`
	CreatedByName string     `json:"createdByName,omitempty"`
	CreatedAt     *time.Time `json:"createdAt,omitempty"`
	ChangeSummary string     `json:"changeSummary,omitempty"`
}

type Comment struct {
	ID              string     `json:"id"`
	DocumentID      string     `json:"documentId"`
	ParentCommentID string     `json:"parentCommentId,omitempty"`
	AuthorID        string     `json:"authorId"`
	AuthorName      string     `json:"authorName,omitempty"`
	Body            string     `json:"body"`
	Resolved        bool       `json:"resolved"`
	ResolvedBy      string     `json:"resolvedBy,omitempty"`
	ResolvedAt      *time.Time `json:"resolvedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       *time.Time `json:"updatedAt,omitempty"`
	Replies         []*Comment `json:"replies"`
}

type Annotation struct {
	ID         string     `json:"id"`
	DocumentID string     `json:"documentId"`
	VersionID  string     `json:"versionId,omitempty"`
	AuthorID   string     `json:"authorId"`
	Page       int32      `json:"page,omitempty"`
	Kind       string     `json:"kind"`
	Payload    string     `json:"payload,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  *time.Time `json:"updatedAt,omitempty"`
}

type WorkflowInstance struct {
	ID             string     `json:"id"`
	DefinitionID   string     `json:"definitionId"`
	DefinitionName string     `json:"definitionName,omitempty"`
	DocumentID     string     `json:"documentId,omitempty"`
	Status         string     `json:"status"`
	StartedAt      time.Time  `json:"startedAt"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
	StartedBy      string     `json:"startedBy,omitempty"`
}

type Task struct {
	ID                 string     `json:"id"`
	WorkflowInstanceID string     `json:"workflowInstanceId"`
	AssigneeID         string     `json:"assigneeId,omitempty"`
	AssigneeName       string     `json:"assigneeName,omitempty"`
	Title              string     `json:"title"`
	Status             string     `json:"status"`
	DueAt              *time.Time `json:"dueAt,omitempty"`
	CompletedAt        *time.Time `json:"completedAt,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
}

type ActivityEvent struct {
	ID         string    `json:"id"`
	DocumentID string    `json:"documentId"`
	Kind       string    `json:"kind"`
	ActorID    string    `json:"actorId,omitempty"`
	ActorName  string    `json:"actorName,omitempty"`
	OccurredAt time.Time `json:"occurredAt"`
	Summary    string    `json:"summary"`
	Payload    string    `json:"payload,omitempty"`
}

// Connection is the page envelope used by every list query.
type Connection[T any] struct {
	Nodes      []T    `json:"nodes"`
	NextCursor string `json:"nextCursor,omitempty"`
	TotalCount int    `json:"totalCount,omitempty"`
}
