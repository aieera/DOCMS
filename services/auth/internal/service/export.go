package service

// Sha256HexForHandler re-exports the internal sha256Hex helper so handler
// code can compute a token hash without duplicating the function (and
// without pulling crypto/sha256 into every handler file). Kept minimal on
// purpose — do not grow this into a general "utils" surface.
func Sha256HexForHandler(s string) string { return sha256Hex(s) }
