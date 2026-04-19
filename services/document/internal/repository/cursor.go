package repository

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// cursor represents the position between two pages for keyset pagination.
// The Sort field echoes the ORDER BY column the cursor was built against,
// so a caller can't feed a "sorted by title" cursor into a "sorted by
// created_at" query.
type cursor struct {
	Sort string    `json:"s"`
	Time time.Time `json:"t,omitempty"`
	Text string    `json:"x,omitempty"`
	Size int64     `json:"z,omitempty"`
	ID   uuid.UUID `json:"i"`
}

// encodeCursor base64-url encodes a cursor for wire transport.
func encodeCursor(c cursor) string {
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor reverses encodeCursor. Returns ok=false for empty/invalid
// input; callers should treat that as "no cursor" (first page).
func decodeCursor(token string) (cursor, bool) {
	if token == "" {
		return cursor{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return cursor{}, false
	}
	var c cursor
	if err := json.Unmarshal(b, &c); err != nil {
		return cursor{}, false
	}
	return c, true
}

// clampPageSize enforces the contract documented in PaginationRequest.
func clampPageSize(requested int) int {
	switch {
	case requested <= 0:
		return 20
	case requested > 100:
		return 100
	default:
		return requested
	}
}

// sortColumn validates and maps the client-supplied sort field to a safe
// column name. Rejecting unknown values keeps SQL injection off the menu.
func sortColumn(s string) (string, error) {
	switch s {
	case "", "created_at":
		return "created_at", nil
	case "updated_at":
		return "updated_at", nil
	case "title":
		return "title", nil
	case "size_bytes", "total_size_bytes":
		return "total_size_bytes", nil
	}
	return "", fmt.Errorf("invalid sort_by: %q", s)
}
