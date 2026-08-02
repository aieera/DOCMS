package email

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
)

// walkMIME pulls the first text/plain (or text/html if no plain) body
// part out of a parsed message and collects every leaf part that has a
// Content-Disposition: attachment header into Attachment structs.
//
// Recursion depth is bounded loosely by the input — net/mail caps the
// raw size; multipart.Reader streams. Nested multiparts (multipart/
// alternative inside multipart/mixed, etc.) are walked.
func walkMIME(msg *mail.Message) (body string, attachments []Attachment, err error) {
	mediaType, params, parseErr := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if parseErr != nil || !strings.HasPrefix(mediaType, "multipart/") {
		// Single-part message — body is the whole thing.
		raw := readDecoded(msg.Body, msg.Header.Get("Content-Transfer-Encoding"))
		return string(raw), nil, nil
	}
	boundary := params["boundary"]
	if boundary == "" {
		return "", nil, fmt.Errorf("missing multipart boundary")
	}
	mr := multipart.NewReader(msg.Body, boundary)
	body, attachments = walkParts(mr, "", nil)
	return body, attachments, nil
}

// readDecoded reads a MIME part applying its Content-Transfer-Encoding.
//
// mime/multipart deliberately does NOT do this for you: NextPart hands
// back the part's bytes verbatim. Reading a base64 part with io.ReadAll
// therefore yields the base64 TEXT, so an ingested PDF was stored as
// "JVBERi0x..." (the encoding of "%PDF-1") — unreadable to both the
// OCR pipeline and the browser's PDF viewer.
//
// On a decode error we fall back to the raw bytes: a mislabelled part is
// better filed as-is than dropped.
func readDecoded(r io.Reader, encoding string) []byte {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		raw, _ := io.ReadAll(r)
		// MUAs hard-wrap base64 at 76 columns; the decoder rejects the
		// embedded CRLFs, so strip all whitespace first.
		clean := strings.Map(func(c rune) rune {
			if c == '\r' || c == '\n' || c == ' ' || c == '\t' {
				return -1
			}
			return c
		}, string(raw))
		// Some senders omit the trailing "=" padding.
		if dec, err := base64.StdEncoding.WithPadding(base64.NoPadding).DecodeString(strings.TrimRight(clean, "=")); err == nil {
			return dec
		}
		return raw
	case "quoted-printable":
		dec, err := io.ReadAll(quotedprintable.NewReader(r))
		if err != nil && len(dec) == 0 {
			return nil
		}
		return dec
	default: // 7bit, 8bit, binary, or absent — already plain.
		raw, _ := io.ReadAll(r)
		return raw
	}
}

// decodeHeader resolves RFC 2047 encoded-words ("=?UTF-8?q?...?=") that
// mail clients use for non-ASCII header text. Without it a subject line
// became the document's literal title, e.g. "=?UTF-8?q?[Docker]_You?=.md".
// Undecodable input is returned unchanged rather than dropped.
func decodeHeader(s string) string {
	if s == "" || !strings.Contains(s, "=?") {
		return s
	}
	if dec, err := (&mime.WordDecoder{}).DecodeHeader(s); err == nil {
		return dec
	}
	return s
}

func walkParts(mr *multipart.Reader, body string, attachments []Attachment) (string, []Attachment) {
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		ct := part.Header.Get("Content-Type")
		mediaType, params, _ := mime.ParseMediaType(ct)
		cd := part.Header.Get("Content-Disposition")
		dispMediaType, dispParams, _ := mime.ParseMediaType(cd)

		// Attachment: any leaf with Content-Disposition: attachment, OR a
		// non-text leaf with a filename in the content-type params.
		filename := dispParams["filename"]
		if filename == "" {
			filename = params["name"]
		}
		// Non-ASCII filenames arrive as encoded-words too.
		filename = decodeHeader(filename)
		isAttachment := dispMediaType == "attachment" || (filename != "" && !strings.HasPrefix(mediaType, "text/"))

		if strings.HasPrefix(mediaType, "multipart/") {
			inner := multipart.NewReader(part, params["boundary"])
			body, attachments = walkParts(inner, body, attachments)
			continue
		}

		raw := readDecoded(part, part.Header.Get("Content-Transfer-Encoding"))
		if isAttachment {
			attachments = append(attachments, Attachment{
				Filename:    filename,
				ContentType: mediaType,
				Bytes:       raw,
			})
			continue
		}
		// Body part. Prefer text/plain; fall back to text/html only if
		// we didn't already take a text/plain.
		if mediaType == "text/plain" && body == "" {
			body = string(raw)
		} else if mediaType == "text/html" && body == "" {
			body = string(raw)
		}
	}
	return body, attachments
}
