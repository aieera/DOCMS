package testutil

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Organization is the minimal tenant shape used by fixtures. Real services
// model this in their own packages; these factories exist only to produce
// deterministic-ish values for tests.
type Organization struct {
	ID        uuid.UUID
	Slug      string
	Name      string
	Region    string
	CreatedAt time.Time
}

// User is the minimal user shape.
type User struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Email    string
	Role     string
}

// Workspace is the minimal workspace shape.
type Workspace struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Name     string
}

// Folder is the minimal folder shape.
type Folder struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	WorkspaceID uuid.UUID
	Path        string // ltree-style, e.g. "root.engineering.specs"
	Name        string
}

// Document is the minimal document shape.
type Document struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	WorkspaceID uuid.UUID
	FolderID    uuid.UUID
	Title       string
	RegionPin   string
	CreatedAt   time.Time
}

// NewTestTenant returns an org with a random slug.
func NewTestTenant() Organization {
	id := mustV7()
	return Organization{
		ID:        id,
		Slug:      fmt.Sprintf("tenant-%s", id.String()[:8]),
		Name:      "Test Tenant",
		Region:    "us-east-1",
		CreatedAt: time.Now().UTC(),
	}
}

// NewTestUser returns a user for the given tenant.
func NewTestUser(tenantID uuid.UUID) User {
	id := mustV7()
	return User{
		ID:       id,
		TenantID: tenantID,
		Email:    fmt.Sprintf("user-%s@example.test", id.String()[:8]),
		Role:     "member",
	}
}

// NewTestWorkspace returns a workspace for the given tenant.
func NewTestWorkspace(tenantID uuid.UUID) Workspace {
	return Workspace{ID: mustV7(), TenantID: tenantID, Name: "Test Workspace"}
}

// NewTestFolder returns a folder under workspaceID.
func NewTestFolder(tenantID, workspaceID uuid.UUID) Folder {
	id := mustV7()
	return Folder{
		ID:          id,
		TenantID:    tenantID,
		WorkspaceID: workspaceID,
		Path:        fmt.Sprintf("root.test_%s", id.String()[:8]),
		Name:        "Test Folder",
	}
}

// NewTestDocument returns a document under folderID.
func NewTestDocument(tenantID, workspaceID, folderID uuid.UUID) Document {
	return Document{
		ID:          mustV7(),
		TenantID:    tenantID,
		WorkspaceID: workspaceID,
		FolderID:    folderID,
		Title:       "Test Document",
		RegionPin:   "us-east-1",
		CreatedAt:   time.Now().UTC(),
	}
}

func mustV7() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		panic(err)
	}
	return id
}
