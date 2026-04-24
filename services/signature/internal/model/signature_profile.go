package model

import (
	"time"

	"github.com/google/uuid"
)

// SignatureProfileKind is the rendering source: drawn on a canvas,
// uploaded from a raster image, or typed with a preset font.
type SignatureProfileKind string

const (
	ProfileKindDraw   SignatureProfileKind = "draw"
	ProfileKindUpload SignatureProfileKind = "upload"
	ProfileKindTyped  SignatureProfileKind = "typed"
)

// SignatureProfile mirrors the signature_profiles row.
type SignatureProfile struct {
	TenantID       uuid.UUID
	ID             uuid.UUID
	UserID         uuid.UUID
	Name           string
	Kind           SignatureProfileKind
	FontStyle      string // only for ProfileKindTyped
	ImageRef       string // S3 key
	InitialsRef    string
	WrappedDEK     []byte
	KEKID          string
	Nonce          []byte // AES-GCM nonce for the ciphertext at ImageRef
	ImageSizeBytes int
	IsDefault      bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
	RevokedAt      *time.Time
}

// PublicView is the safe projection for API responses — strips
// wrapped key material and nonce. Image bytes are fetched via a
// dedicated endpoint that re-runs authz.
type SignatureProfilePublicView struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	FontStyle string    `json:"font_style,omitempty"`
	IsDefault bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ToPublic returns the scrubbed view.
func (p *SignatureProfile) ToPublic() SignatureProfilePublicView {
	return SignatureProfilePublicView{
		ID:        p.ID,
		Name:      p.Name,
		Kind:      string(p.Kind),
		FontStyle: p.FontStyle,
		IsDefault: p.IsDefault,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}
