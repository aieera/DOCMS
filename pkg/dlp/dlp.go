// Package dlp implements a Data Loss Prevention pipeline that scans
// document content on share/download for sensitive patterns (credit cards,
// SSNs, custom keywords). Actions: warn, block, quarantine.
package dlp

import (
	"context"
	"regexp"
	"strings"
)

// Action determines what happens when a pattern matches.
type Action string

const (
	ActionWarn       Action = "warn"
	ActionBlock      Action = "block"
	ActionQuarantine Action = "quarantine"
)

// Rule is a single DLP detection rule.
type Rule struct {
	Name    string
	Pattern *regexp.Regexp
	Action  Action
}

// Match describes a single detection hit.
type Match struct {
	RuleName string `json:"rule_name"`
	Value    string `json:"value"`
	Offset   int    `json:"offset"`
	Action   Action `json:"action"`
}

// ScanResult is the output of a DLP scan.
type ScanResult struct {
	Matches      []Match `json:"matches"`
	BlockReason  string  `json:"block_reason,omitempty"`
	ShouldBlock  bool    `json:"should_block"`
	ShouldWarn   bool    `json:"should_warn"`
	Quarantined  bool    `json:"quarantined"`
}

// DefaultRules are the built-in PII/PCI patterns.
var DefaultRules = []Rule{
	{Name: "credit_card", Pattern: regexp.MustCompile(`\b(?:\d{4}[\s-]?){3}\d{4}\b`), Action: ActionBlock},
	{Name: "ssn", Pattern: regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`), Action: ActionBlock},
	{Name: "iban", Pattern: regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{4}\d{7}([A-Z0-9]?){0,16}\b`), Action: ActionWarn},
	{Name: "email_bulk", Pattern: regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}\b`), Action: ActionWarn},
}

// Scanner scans text content against DLP rules.
type Scanner struct {
	rules []Rule
}

// NewScanner creates a Scanner with the given rules. If nil, uses DefaultRules.
func NewScanner(rules []Rule) *Scanner {
	if rules == nil {
		rules = DefaultRules
	}
	return &Scanner{rules: rules}
}

// AddCustomKeywords adds keyword-based block rules (per-tenant).
func (s *Scanner) AddCustomKeywords(keywords []string, action Action) {
	for _, kw := range keywords {
		if kw == "" {
			continue
		}
		escaped := regexp.QuoteMeta(strings.ToLower(kw))
		s.rules = append(s.rules, Rule{
			Name:    "keyword:" + kw,
			Pattern: regexp.MustCompile(`(?i)\b` + escaped + `\b`),
			Action:  action,
		})
	}
}

// Scan runs all rules against the text and returns the result.
func (s *Scanner) Scan(_ context.Context, text string) *ScanResult {
	result := &ScanResult{}
	for _, rule := range s.rules {
		locs := rule.Pattern.FindAllStringIndex(text, 100) // cap at 100 matches per rule
		for _, loc := range locs {
			value := text[loc[0]:loc[1]]
			// Mask the value for logging (show first 4 and last 2 chars).
			masked := maskValue(value)
			match := Match{
				RuleName: rule.Name,
				Value:    masked,
				Offset:   loc[0],
				Action:   rule.Action,
			}
			result.Matches = append(result.Matches, match)
			switch rule.Action {
			case ActionBlock:
				result.ShouldBlock = true
				result.BlockReason = "DLP rule " + rule.Name + " matched"
			case ActionQuarantine:
				result.Quarantined = true
			case ActionWarn:
				result.ShouldWarn = true
			}
		}
	}
	return result
}

func maskValue(s string) string {
	if len(s) <= 6 {
		return "****"
	}
	return s[:4] + strings.Repeat("*", len(s)-6) + s[len(s)-2:]
}
