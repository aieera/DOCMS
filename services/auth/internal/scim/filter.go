package scim

import (
	"regexp"
	"strings"
)

// Filter is a tiny SCIM-filter evaluator. RFC 7644 §3.4.2.2 specifies a
// rich grammar; we support only the handful of shapes IdPs actually emit:
//
//	attr eq "value"
//	attr ne "value"
//	attr sw "value"     (startsWith)
//	attr co "value"     (contains)
//	attr pr              (present, non-null — we treat as no-op for the
//	                      attributes we expose)
//
// Conjunctions (and/or) and bracketed expressions are NOT supported and
// will be rejected with 501 Not Implemented.
type Filter struct {
	Attr  string
	Op    string
	Value string
}

// filterRE matches `attr op "value"` with the operators we support.
var filterRE = regexp.MustCompile(`(?i)^([a-z][a-z0-9._]*)\s+(eq|ne|sw|co|pr)(?:\s+"([^"]*)")?\s*$`)

// ParseFilter returns nil (no filter) for empty input, a *Filter on the
// successful parse, and an error on anything we don't support.
func ParseFilter(raw string) (*Filter, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.ContainsAny(raw, "()") || containsFold(raw, " and ") || containsFold(raw, " or ") {
		return nil, errUnsupported("conjunctions and grouping not supported")
	}
	m := filterRE.FindStringSubmatch(raw)
	if m == nil {
		return nil, errUnsupported("filter syntax not recognized")
	}
	return &Filter{Attr: strings.ToLower(m[1]), Op: strings.ToLower(m[2]), Value: m[3]}, nil
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// unsupportedError is returned for parseable-but-unsupported inputs.
type unsupportedError struct{ msg string }

func (e unsupportedError) Error() string { return e.msg }

// IsUnsupported lets handlers respond with 501 instead of 400 per RFC 7644
// recommendations ("scimType":"tooMany" for huge requests, etc.).
func IsUnsupported(err error) bool {
	_, ok := err.(unsupportedError)
	return ok
}

func errUnsupported(m string) error { return unsupportedError{msg: m} }
