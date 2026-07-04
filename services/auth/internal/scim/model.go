// Package scim implements the subset of SCIM 2.0 (RFC 7643/7644) needed to
// interoperate with enterprise identity providers (Okta, Azure AD, OneLogin,
// JumpCloud). Complete spec compliance is a long tail; this package covers
// the paths those IdPs actually use:
//   - /Users        GET (filter), POST, GET/:id, PUT/:id, PATCH/:id, DELETE/:id
//   - /Groups       GET (filter), POST, GET/:id, PATCH/:id, DELETE/:id
//   - /ServiceProviderConfig, /ResourceTypes, /Schemas   (static, cacheable)
//
// Each tenant presents its own /scim/v2 surface authenticated by an opaque
// bearer token stored (hashed) in sso_configs alongside its SAML/OIDC
// config. Configuration is admin-managed via SQL for now; a provisioning
// UI lands in a follow-up.
package scim

import "time"

// ---- Protocol schemas (URIs) ----------------------------------------------
const (
	SchemaUser     = "urn:ietf:params:scim:schemas:core:2.0:User"
	SchemaGroup    = "urn:ietf:params:scim:schemas:core:2.0:Group"
	SchemaListResp = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	SchemaError    = "urn:ietf:params:scim:api:messages:2.0:Error"
	SchemaPatchOp  = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	SchemaSPConfig = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"
	SchemaResType  = "urn:ietf:params:scim:schemas:core:2.0:ResourceType"
)

// ---- Common envelope fields -----------------------------------------------

// Meta is the SCIM `meta` object on every resource.
type Meta struct {
	ResourceType string    `json:"resourceType"`
	Created      time.Time `json:"created"`
	LastModified time.Time `json:"lastModified"`
	Location     string    `json:"location,omitempty"`
	Version      string    `json:"version,omitempty"`
}

// ---- User -----------------------------------------------------------------

// Name is the SCIM name sub-object (only formatted used here; IdPs that
// only emit givenName/familyName still work because clients echo what we
// return).
type Name struct {
	Formatted  string `json:"formatted,omitempty"`
	GivenName  string `json:"givenName,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
}

// Email is one entry in the multi-valued `emails` array.
type Email struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary,omitempty"`
	Type    string `json:"type,omitempty"`
}

// User is the SCIM-wire projection of our internal user.
type User struct {
	Schemas     []string `json:"schemas"`
	ID          string   `json:"id"`
	ExternalID  string   `json:"externalId,omitempty"`
	UserName    string   `json:"userName"`
	DisplayName string   `json:"displayName,omitempty"`
	Name        *Name    `json:"name,omitempty"`
	Emails      []Email  `json:"emails,omitempty"`
	Active      bool     `json:"active"`
	Meta        Meta     `json:"meta"`
}

// ---- Group ----------------------------------------------------------------

// GroupMember is one entry in `Group.members`.
type GroupMember struct {
	Value   string `json:"value"`             // user id
	Display string `json:"display,omitempty"` // display_name
	Type    string `json:"type,omitempty"`    // "User" | "Group"
	Ref     string `json:"$ref,omitempty"`
}

// Group maps to our `groups` + `group_members` tables.
type Group struct {
	Schemas     []string      `json:"schemas"`
	ID          string        `json:"id"`
	ExternalID  string        `json:"externalId,omitempty"`
	DisplayName string        `json:"displayName"`
	Members     []GroupMember `json:"members,omitempty"`
	Meta        Meta          `json:"meta"`
}

// ---- List response envelope -----------------------------------------------

// ListResponse is the RFC 7644 §3.4.2 envelope for collection queries.
type ListResponse[T any] struct {
	Schemas      []string `json:"schemas"`
	TotalResults int      `json:"totalResults"`
	StartIndex   int      `json:"startIndex"`
	ItemsPerPage int      `json:"itemsPerPage"`
	Resources    []T      `json:"Resources"`
}

// ---- Error envelope -------------------------------------------------------

// Error is the RFC 7644 §3.12 error body.
type Error struct {
	Schemas  []string `json:"schemas"`
	Status   string   `json:"status"` // numeric string per spec
	ScimType string   `json:"scimType,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

// NewError builds a conforming Error.
func NewError(status int, scimType, detail string) *Error {
	return &Error{
		Schemas:  []string{SchemaError},
		Status:   itoa(status),
		ScimType: scimType,
		Detail:   detail,
	}
}

// ---- PATCH operation ------------------------------------------------------

// PatchOp is the envelope for PATCH requests.
type PatchOp struct {
	Schemas    []string         `json:"schemas"`
	Operations []PatchOperation `json:"Operations"`
}

// PatchOperation is one entry in the ops array.
type PatchOperation struct {
	Op    string `json:"op"` // "add" | "replace" | "remove"
	Path  string `json:"path,omitempty"`
	Value any    `json:"value,omitempty"`
}

// ---- small int→string helper (avoid strconv dep in this file) -----------

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}
