package email

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
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
		raw, _ := io.ReadAll(msg.Body)
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
		isAttachment := dispMediaType == "attachment" || (filename != "" && !strings.HasPrefix(mediaType, "text/"))

		if strings.HasPrefix(mediaType, "multipart/") {
			inner := multipart.NewReader(part, params["boundary"])
			body, attachments = walkParts(inner, body, attachments)
			continue
		}

		raw, _ := io.ReadAll(part)
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