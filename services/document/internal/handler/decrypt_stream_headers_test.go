package handler

import "testing"

// The document viewer renders a preview by pointing an <iframe> at the
// decrypt-stream endpoint, which is same-origin. The global security
// headers send X-Frame-Options: DENY, and DENY blocks same-origin
// framing too — shipping it broke every document preview in the product
// until decrypt-stream started overriding it to SAMEORIGIN.
//
// These tests pin the rule the handler encodes: a type that renders
// inline must be framable, and a type that does not render inline must
// never be served inline at all (an uploaded .html or .svg would
// otherwise execute in our origin).
func TestInlineSafeMimeCoversPreviewableTypes(t *testing.T) {
	// Everything the viewer frames or <img>s must be inline-safe, or the
	// preview silently becomes a download.
	for _, mime := range []string{
		"application/pdf",
		"image/png",
		"image/jpeg",
		"image/gif",
		"image/webp",
	} {
		if !inlineSafeMime(mime) {
			t.Errorf("%s must be inline-safe: the viewer previews it", mime)
		}
	}
}

func TestInlineSafeMimeRejectsScriptableTypes(t *testing.T) {
	// These are the stored-XSS carriers. Served inline from our origin
	// they execute with the user's session.
	for _, mime := range []string{
		"text/html",
		"image/svg+xml",
		"application/xhtml+xml",
		"text/xml",
		"application/javascript",
	} {
		if inlineSafeMime(mime) {
			t.Errorf("%s must NOT be inline-safe: it can script in our origin", mime)
		}
	}
}
