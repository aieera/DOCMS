package email

import (
	"errors"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2/imapclient"
)

// N-9 follow-up: the body is fetched as a partial range of cap+1 bytes. A
// server that under-reports RFC822.SIZE returns more than the cap; that
// message must fail, not be parsed truncated and filed as complete.
func TestIMAP_UnderReportedSizeFails(t *testing.T) {
	const max = 64
	raw := "Subject: big\r\nContent-Type: text/plain\r\n\r\n" + strings.Repeat("x", 200)
	buf := &imapclient.FetchMessageBuffer{
		UID:         7,
		RFC822Size:  10, // lies
		BodySection: []imapclient.FetchBodySectionBuffer{{Bytes: []byte(raw[:max+1])}},
	}
	if _, err := parseIMAPMessage(buf, 1, max); !errors.Is(err, errMessageTooLarge) {
		t.Fatalf("truncated message parsed: err=%v", err)
	}
	small := "Subject: ok\r\nContent-Type: text/plain\r\n\r\nhi\r\n"
	buf.BodySection = []imapclient.FetchBodySectionBuffer{{Bytes: []byte(small)}}
	env, err := parseIMAPMessage(buf, 1, max)
	if err != nil || env.Subject != "ok" {
		t.Fatalf("message under the cap: env=%+v err=%v", env, err)
	}
}
