// ADR 0067 — invariants for the widened annotation type set.
//
// Pure-function tests; no DB needed.
package model

import "testing"

func TestIsValidAnnotationType_LegacyValues(t *testing.T) {
	for _, v := range []string{"highlight", "note", "stamp", "drawing"} {
		if !IsValidAnnotationType(v) {
			t.Errorf("legacy value %q must remain valid (back-compat)", v)
		}
	}
}

func TestIsValidAnnotationType_ADR0076Categories(t *testing.T) {
	for _, v := range []string{"pdf_markup", "image_shape", "video_timestamp"} {
		if !IsValidAnnotationType(v) {
			t.Errorf("ADR 0067 category %q must be valid", v)
		}
	}
}

func TestIsValidAnnotationType_RejectsUnknown(t *testing.T) {
	for _, v := range []string{"", "redaction", "comment", "smoke", "PDF_MARKUP"} {
		if IsValidAnnotationType(v) {
			t.Errorf("unknown value %q must be rejected (case-sensitive enum)", v)
		}
	}
}

func TestAnnotationLayer_RoutesByType(t *testing.T) {
	cases := map[string]string{
		// Image / video map to their dedicated overlay layers.
		AnnotationTypeImageShape:     "image",
		AnnotationTypeVideoTimestamp: "video",
		// Everything else (legacy primitives + pdf_markup) renders
		// on the PDF overlay.
		AnnotationTypePDFMarkup: "pdf",
		AnnotationTypeHighlight: "pdf",
		AnnotationTypeNote:      "pdf",
		AnnotationTypeStamp:     "pdf",
		AnnotationTypeDrawing:   "pdf",
		// Unknown also falls back to the pdf layer (same fail-open
		// behaviour used by the renderer when a row carries a
		// future type from a newer client).
		"unknown": "pdf",
	}
	for in, want := range cases {
		if got := AnnotationLayer(in); got != want {
			t.Errorf("AnnotationLayer(%q) = %q want %q", in, got, want)
		}
	}
}
