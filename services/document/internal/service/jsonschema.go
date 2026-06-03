package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// schemaCache memoizes compiled JSON-Schemas keyed by the SHA-256 of their
// raw bytes. Compilation is non-trivial (reflection + walks) and tenant
// schemas change infrequently, so the cache amortizes the cost across all
// document writes for that tenant.
//
// Note: this cache is process-local. A new tenant schema becomes effective
// here once the writer's process re-reads the schema bytes from Postgres
// (the `MetadataSchema.Get` repo call). Cross-process invalidation is not
// needed because each process keys by content-hash; an updated schema
// hashes to a new key, so stale compiled schemas are inert.
var schemaCache = struct {
	sync.RWMutex
	m map[string]*jsonschema.Schema
}{m: map[string]*jsonschema.Schema{}}

// validateMetadataAgainstFullSchema runs the full draft-07/2019-09/2020-12
// JSON-Schema validator from santhosh-tekuri/jsonschema. Falls back to the
// hand-rolled validator above when the bytes don't compile (preserves
// behavior on legacy / corrupt schemas).
//
// enforceRequired matches the contract documented on the basic validator.
func validateMetadataAgainstFullSchema(schemaJSON []byte, metadata map[string]any, enforceRequired bool) error {
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

	// If draft consumers do not want required-fields enforced, strip the
	// "required" key from a copy of the schema before compilation. Cheaper
	// than running two compilations per tenant.
	effectiveBytes := schemaJSON
	if !enforceRequired {
		effectiveBytes = stripRequired(schemaJSON)
	}

	schema, err := compileCached(effectiveBytes)
	if err != nil {
		// Schema doesn't compile — fall back to the permissive legacy path
		// so a corrupt schema doesn't block all writes.
		return validateMetadataAgainstSchemaOpts(schemaJSON, metadata, enforceRequired)
	}
	if err := schema.Validate(metadata); err != nil {
		return vdmserr.Validation("custom_metadata", "schema validation failed: "+summarize(err))
	}
	return nil
}

// compileCached returns a compiled schema for `raw`, memoized by content
// hash. Concurrent compilations of the same schema may both run once
// (single-flight is overkill here; jsonschema.Compile is cheap and idempotent).
func compileCached(raw []byte) (*jsonschema.Schema, error) {
	key := contentHash(raw)
	schemaCache.RLock()
	cached, ok := schemaCache.m[key]
	schemaCache.RUnlock()
	if ok {
		return cached, nil
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("custom_metadata.json", bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("add resource: %w", err)
	}
	s, err := c.Compile("custom_metadata.json")
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	schemaCache.Lock()
	schemaCache.m[key] = s
	schemaCache.Unlock()
	return s, nil
}

// stripRequired returns a copy of schemaJSON with the top-level "required"
// key removed. JSON-Schema treats absent "required" as no required keys.
// We deliberately don't recurse into nested objects — required-fields on
// nested objects (a "billing.address.zip" for example) are still enforced
// even on draft writes, which matches user intent (the nested object is
// either present and complete or absent).
func stripRequired(schemaJSON []byte) []byte {
	var doc map[string]any
	if err := json.Unmarshal(schemaJSON, &doc); err != nil {
		return schemaJSON
	}
	delete(doc, "required")
	out, err := json.Marshal(doc)
	if err != nil {
		return schemaJSON
	}
	return out
}

// contentHash returns a stable cache key for a byte slice. SHA-256 hex
// would be 64 chars; we use a faster (non-crypto) FNV-64 since this is
// just a map key, not a security boundary.
func contentHash(b []byte) string {
	const fnvOffset uint64 = 14695981039346656037
	const fnvPrime uint64 = 1099511628211
	h := fnvOffset
	for _, c := range b {
		h ^= uint64(c)
		h *= fnvPrime
	}
	return fmt.Sprintf("%016x", h)
}

// summarize collapses a santhosh-tekuri ValidationError tree into a short
// one-line message. The full error tree is verbose; users only need to
// know which top-level field failed.
func summarize(err error) string {
	msg := err.Error()
	// Take the first line; jsonschema prints a multi-line tree on conflict.
	if i := strings.IndexByte(msg, '\n'); i > 0 {
		msg = msg[:i]
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}
