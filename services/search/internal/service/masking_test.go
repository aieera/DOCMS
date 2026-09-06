package service

import (
	"testing"

	"github.com/aieera/sedoc/services/search/internal/opensearch"
)

// QA SD-22: search snippets printed unmasked card numbers and SSNs —
// the same document the PII console flags Critical. Everything mapHit
// emits (snippet, description, highlight fragments) must mask payment
// cards (PCI: first six + last four may remain) and SSNs on the way out.

func TestMaskSensitive_CardAndSSN(t *testing.T) {
	in := "Contact john.doe@example.com SSN 123-45-6789 card 4111 1111 1111 1111"
	got := MaskSensitive(in)
	if got != "Contact john.doe@example.com SSN ***-**-6789 card 4111 11** **** 1111" {
		t.Errorf("got %q", got)
	}
}

func TestMaskSensitive_LeavesNonLuhnDigitsAlone(t *testing.T) {
	// 16 digits that fail the Luhn check — an order id, not a card.
	in := "order 1234 5678 9012 3457 shipped"
	if got := MaskSensitive(in); got != in {
		t.Errorf("non-card digits must not be mangled; got %q", got)
	}
}

func TestMaskSensitive_PlainTextUntouched(t *testing.T) {
	in := "The Supplier shall indemnify the Customer."
	if got := MaskSensitive(in); got != in {
		t.Errorf("got %q", got)
	}
}

func TestMaskSensitive_SeesThroughHighlightTags(t *testing.T) {
	// OpenSearch highlight fragments interleave <em> tags inside the
	// text — masking must treat tags as transparent and keep them.
	in := "SSN <em>123-45-6789</em> card <em>4111</em> 1111 1111 1111"
	got := MaskSensitive(in)
	want := "SSN <em>***-**-6789</em> card <em>4111</em> 11** **** 1111"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMapHit_MasksSnippetAndHighlights(t *testing.T) {
	h := opensearch.RawHit{
		Source: map[string]any{
			"document_id":     "d1",
			"title":           "Test One Two.txt",
			"content_snippet": "SSN 123-45-6789 card 4111 1111 1111 1111",
			"description":     "cc 4111-1111-1111-1111",
		},
		Highlight: map[string][]string{
			"content": {"card <em>4111</em> 1111 1111 1111 on file"},
		},
	}
	hit := mapHit(h)
	if hit.ContentSnippet != "SSN ***-**-6789 card 4111 11** **** 1111" {
		t.Errorf("snippet not masked: %q", hit.ContentSnippet)
	}
	if hit.Description != "cc 4111-11**-****-1111" {
		t.Errorf("description not masked: %q", hit.Description)
	}
	if got := hit.Highlights["content"][0]; got != "card <em>4111</em> 11** **** 1111 on file" {
		t.Errorf("highlight not masked: %q", got)
	}
}
