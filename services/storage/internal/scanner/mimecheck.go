package scanner

import (
	"bytes"
	"io"
	"strings"

	"github.com/h2non/filetype"
)

// ExecutableMIMEBlocklist is the set of MIME patterns we refuse at initiate
// time. These cover both the nominal content-type headers a client would
// send AND the magic-byte detections we run on complete.
//
// Note: we check by MIME type AND by extension at initiate. At complete,
// we re-check via magic bytes and quarantine if the real type is in this
// list even when the declared type was benign.
var ExecutableMIMEBlocklist = map[string]struct{}{
	"application/x-msdownload":      {}, // .exe, .dll
	"application/x-executable":      {},
	"application/x-dosexec":         {},
	"application/vnd.microsoft.portable-executable": {},
	"application/x-msdos-program":   {},
	"application/x-ms-installer":    {}, // .msi
	"application/x-ms-shortcut":     {}, // .lnk
	"application/x-bat":             {}, // .bat
	"application/x-sh":              {}, // .sh
	"application/x-python-code":     {}, // .pyc
	"application/vnd.ms-cab-compressed": {},
}

// ExecutableExtensions are the filename suffixes we refuse. Checked in
// addition to the MIME blocklist because some uploads arrive with generic
// content-type (application/octet-stream).
var ExecutableExtensions = map[string]struct{}{
	".exe": {}, ".bat": {}, ".cmd": {}, ".sh": {}, ".ps1": {},
	".com": {}, ".scr": {}, ".pif": {}, ".msi": {}, ".dll": {},
	".sys": {}, ".vbs": {}, ".js": {}, ".jar": {}, ".app": {},
}

// IsBlockedMIME returns true when the declared MIME is in our reject list.
func IsBlockedMIME(m string) bool {
	if m == "" {
		return false
	}
	_, bad := ExecutableMIMEBlocklist[strings.ToLower(strings.TrimSpace(m))]
	return bad
}

// IsBlockedExtension returns true when the filename ends in a blocked suffix.
// Does not strip query strings — callers pass the raw filename, not a URL.
func IsBlockedExtension(filename string) bool {
	filename = strings.ToLower(filename)
	for ext := range ExecutableExtensions {
		if strings.HasSuffix(filename, ext) {
			return true
		}
	}
	return false
}

// DetectedType carries the magic-byte detection result. Extension and MIME
// are populated when filetype successfully identifies the bytes; otherwise
// both are empty and IsKnown=false.
type DetectedType struct {
	Extension string
	MIME      string
	IsKnown   bool
}

// DetectFromReader reads up to 262 bytes from r (the size filetype needs
// to cover every signature it knows) and returns the detected type. The
// reader is NOT rewound — callers pre-buffer if they need to re-read.
func DetectFromReader(r io.Reader) (DetectedType, error) {
	head := make([]byte, 262)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return DetectedType{}, err
	}
	return DetectFromBytes(head[:n])
}

// DetectFromBytes runs h2non/filetype on the provided bytes.
func DetectFromBytes(head []byte) (DetectedType, error) {
	if len(head) == 0 {
		return DetectedType{}, nil
	}
	kind, err := filetype.Match(head)
	if err != nil {
		return DetectedType{}, err
	}
	if kind == filetype.Unknown {
		return DetectedType{IsKnown: false}, nil
	}
	return DetectedType{
		Extension: "." + kind.Extension,
		MIME:      kind.MIME.Value,
		IsKnown:   true,
	}, nil
}

// MIMEMatchesDeclared compares declared vs detected at the subtype level.
// `application/pdf` == `application/pdf` trivially; `image/jpeg` matches
// `image/jpg` (common mis-declare) via the extension fallback.
//
// When declared is empty, any detection is accepted (legacy path).
func MIMEMatchesDeclared(declared string, detected DetectedType) bool {
	if declared == "" || !detected.IsKnown {
		return true
	}
	if strings.EqualFold(declared, detected.MIME) {
		return true
	}
	// Known aliases / common mis-declares.
	aliases := map[string][]string{
		"image/jpeg": {"image/jpg", "image/pjpeg"},
		"image/png":  {"image/x-png"},
		"text/plain": {"application/x-empty"},
	}
	for canonical, alts := range aliases {
		if strings.EqualFold(detected.MIME, canonical) {
			for _, a := range alts {
				if strings.EqualFold(declared, a) {
					return true
				}
			}
		}
	}
	return false
}

// PeekHead reads up to 262 bytes from r into a buffer and returns the
// buffer + a new reader that includes those bytes followed by the rest of
// r. Used when the caller needs both magic-byte detection AND to forward
// the full stream downstream (scan, hash, encrypt) without losing the head.
func PeekHead(r io.Reader) ([]byte, io.Reader, error) {
	head := make([]byte, 262)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, nil, err
	}
	head = head[:n]
	return head, io.MultiReader(bytes.NewReader(head), r), nil
}
