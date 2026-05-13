package email

import (
	"encoding/base64"
	"fmt"
	"strings"
)

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func base64Of(b any) string {
	switch v := b.(type) {
	case string:
		return base64.StdEncoding.EncodeToString([]byte(v))
	case []byte:
		return base64.StdEncoding.EncodeToString(v)
	}
	return ""
}

// formatBody renders an envelope as a Markdown document for ingestion.
// Headers go in a frontmatter-style fence so they're searchable on a
// per-field basis (the search service tokenises markdown).
func formatBody(env *Envelope) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "from: %s\n", env.From)
	if len(env.To) > 0 {
		fmt.Fprintf(&b, "to: %s\n", strings.Join(env.To, ", "))
	}
	if !env.Date.IsZero() {
		fmt.Fprintf(&b, "date: %s\n", env.Date.Format("2006-01-02 15:04:05 -0700"))
	}
	if env.Subject != "" {
		fmt.Fprintf(&b, "subject: %s\n", env.Subject)
	}
	if env.ThreadID != "" {
		fmt.Fprintf(&b, "thread: %s\n", env.ThreadID)
	}
	if len(env.Attachments) > 0 {
		fmt.Fprintf(&b, "attachments: %d\n", len(env.Attachments))
	}
	b.WriteString("---\n\n")
	if env.BodyText != "" {
		b.WriteString(env.BodyText)
	} else if env.BodyHTML != "" {
		b.WriteString(env.BodyHTML)
	}
	return b.String()
}