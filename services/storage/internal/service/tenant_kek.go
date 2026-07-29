package service

import (
	"strings"

	"github.com/google/uuid"
)

// aliasForTenant returns the canonical per-tenant KEK alias used on
// encrypt + decrypt. ADR 0022: the alias is the full derivation input
// for LocalKeyManager and the lookup key for Vault / AWS KMS managers.
//
// Format: "vaultdms/tenant/<uuid>". Versioned aliases (e.g. for
// post-rotation new encrypts) take the form
// "vaultdms/tenant/<uuid>@v<N>"; that variant is produced by the
// rotation path in cmd/dms-admin/kms.go, not here.
func aliasForTenant(tenantID uuid.UUID) string {
	if tenantID == uuid.Nil {
		// Callers that reach here without a tenant are a bug. Return the EMPTY
		// alias: deriveKEK (LocalKeyManager) rejects "" and Vault/AWS get an empty
		// key name, so the downstream crypto call fails closed — rather than the
		// old "vaultdms/tenant/nil", which every manager happily derived into a
		// single shared, predictable KEK, defeating per-tenant crypto-shred.
		return ""
	}
	return "vaultdms/tenant/" + tenantID.String()
}

// aliasForTenantInRegion returns the region-scoped per-tenant KEK
// alias (Wave 11.7). Callers that upload into a non-default region
// pass the region here so the downstream LocalKeyManager (or Vault
// / AWS KMS) derives / looks up a master-secret scoped to that
// region. An empty region falls back to the plain
// "vaultdms/tenant/<uuid>" form for backwards compatibility with
// pre-regional data.
//
// Format: "vaultdms/tenant/<uuid>/<region>". The slash separator
// keeps both the pre-regional and post-regional forms unambiguous —
// no kekID with an old-style `@v2` rotation suffix can collide.
func aliasForTenantInRegion(tenantID uuid.UUID, region string) string {
	base := aliasForTenant(tenantID)
	region = strings.TrimSpace(region)
	if region == "" {
		return base
	}
	return base + "/" + region
}
