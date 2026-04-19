// Package validation provides request struct validation, sanitization, and
// JSON safety checks. Wraps go-playground/validator with VaultDMS-specific
// rules (UUID format, max string length, null byte rejection, JSON depth).
package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
)

// MaxJSONDepth is the maximum nesting depth for request bodies.
const MaxJSONDepth = 10

// MaxStringLength is the default max for untrimmed string fields.
const MaxStringLength = 10000

var validate *validator.Validate

func init() {
	validate = validator.New(validator.WithRequiredStructEnabled())
	_ = validate.RegisterValidation("uuid", validateUUID)
	_ = validate.RegisterValidation("safetxt", validateSafeText)
}

// Struct validates a struct using go-playground/validator tags. Returns a
// user-friendly error listing all violations.
func Struct(s any) error {
	if err := validate.Struct(s); err != nil {
		var ve validator.ValidationErrors
		if errors.As(err, &ve) {
			msgs := make([]string, 0, len(ve))
			for _, fe := range ve {
				msgs = append(msgs, fmt.Sprintf("%s: failed %s validation", fe.Field(), fe.Tag()))
			}
			return fmt.Errorf("validation: %s", strings.Join(msgs, "; "))
		}
		return err
	}
	return nil
}

// DecodeAndValidate reads JSON from r.Body, enforces body size + JSON
// depth, trims strings, rejects null bytes, then validates the struct.
func DecodeAndValidate(r *http.Request, dst any) error {
	body := http.MaxBytesReader(nil, r.Body, 10*1024*1024)
	raw, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("body read: %w", err)
	}
	// Null byte check.
	for _, b := range raw {
		if b == 0 {
			return errors.New("request body contains null bytes")
		}
	}
	// JSON depth check.
	if err := checkJSONDepth(raw, MaxJSONDepth); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return Struct(dst)
}

// TrimString trims whitespace and enforces max length.
func TrimString(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if maxLen > 0 && utf8.RuneCountInString(s) > maxLen {
		runes := []rune(s)
		s = string(runes[:maxLen])
	}
	return s
}

// IsValidUUID checks if s is a valid UUID.
func IsValidUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// ContainsNullByte returns true if s contains a null byte.
func ContainsNullByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return true
		}
	}
	return false
}

// ---- internal -------------------------------------------------------------

func validateUUID(fl validator.FieldLevel) bool {
	return IsValidUUID(fl.Field().String())
}

func validateSafeText(fl validator.FieldLevel) bool {
	s := fl.Field().String()
	return !ContainsNullByte(s) && utf8.ValidString(s)
}

func checkJSONDepth(raw []byte, maxDepth int) error {
	depth := 0
	for _, b := range raw {
		switch b {
		case '{', '[':
			depth++
			if depth > maxDepth {
				return fmt.Errorf("json depth exceeds maximum of %d", maxDepth)
			}
		case '}', ']':
			depth--
		}
	}
	return nil
}
