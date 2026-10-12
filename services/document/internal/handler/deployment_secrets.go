package handler

import (
	"os"

	"github.com/aieera/sedoc/services/document/internal/service"
)

// ResolveZTPepper resolves SEDOC_ZT_TOKEN_PEPPER under the D-13 rule, keyed
// on the raw SEDOC_ENVIRONMENT (an unset variable is NOT dev). A nil result
// makes the zero-trust routes answer 503.
func ResolveZTPepper() ([]byte, error) {
	return service.ResolveSecret(service.RawEnvironment(), os.Getenv("SEDOC_ZT_TOKEN_PEPPER"), DevZTTokenPepper)
}

// ResolveDSRSalt resolves SEDOC_DSR_ANONYMIZE_SALT under the D-13 rule,
// keyed on the raw SEDOC_ENVIRONMENT. An empty result makes DSR
// anonymisation answer 503.
func ResolveDSRSalt() ([]byte, error) {
	return service.ResolveSecret(service.RawEnvironment(), os.Getenv("SEDOC_DSR_ANONYMIZE_SALT"), DevDSRSalt)
}
