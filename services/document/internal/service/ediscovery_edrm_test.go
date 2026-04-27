package service

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

// EDRM v1.2 LoadFile structural assertions. Pure encoding/xml round-
// trip — does NOT validate against the upstream EDRM XSD (which
// isn't in-repo; ADR 0038 follow-up will add it + a libxml2-driven
// test once the integration suite is up). This test catches the
// regressions that matter day-to-day: missing required attributes,
// dropped UDFs, per-doc tag drift, and shape errors that would
// cause a vendor's loadfile-import tool to reject the bundle.
//
// TODO (follow-up PR): add docs/ediscovery/edrm-v1.2.xsd from
// https://edrm.net/ and a CGO-gated test that runs xmllint --schema.

func TestRenderEDRM_HappyPath(t *testing.T) {
	manifest := sampleManifest()
	xmlBytes, err := RenderEDRM(manifest, EDRMOptions{
		MatterNumber:   "2026-PAT-014",
		MatterName:     "Acme v. Beta — patent litigation",
		ExportID:       "exp-7777",
		CustodianEmail: "alice@acme.test",
	})
	if err != nil {
		t.Fatalf("RenderEDRM: %v", err)
	}
	if !strings.HasPrefix(string(xmlBytes), `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Fatalf("missing XML declaration; got first line: %q", strings.SplitN(string(xmlBytes), "\n", 2)[0])
	}

	var got edrmRoot
	if err := xml.Unmarshal(xmlBytes, &got); err != nil {
		t.Fatalf("unmarshal back: %v", err)
	}

	// Root attributes — vendors switch on DataInterchangeType +
	// MajorVersion / MinorVersion. Wrong values = silent ingest fail.
	if got.DataInterchangeType != "LoadFile" {
		t.Errorf("DataInterchangeType = %q, want LoadFile", got.DataInterchangeType)
	}
	if got.MajorVersion != "1" || got.MinorVersion != "2" {
		t.Errorf("EDRM version = %s.%s, want 1.2", got.MajorVersion, got.MinorVersion)
	}

	// Document count must match the manifest input.
	if got, want := len(got.Batch.Documents.Document), len(manifest.Documents); got != want {
		t.Errorf("Batch.Documents.Document count = %d, want %d", got, want)
	}

	// Required matter-level UDFs MUST be present. Vendor ingest tools
	// commonly key on these.
	requiredUDFs := []string{
		"vaultdms.tenant_id",
		"vaultdms.matter_number",
		"vaultdms.matter_name",
		"vaultdms.export_id",
		"vaultdms.exported_at",
		"vaultdms.custodian_email",
		"vaultdms.exported_by",
		"vaultdms.document_count",
	}
	udfNames := make(map[string]string, len(got.UserDefinedFields.UserDefinedField))
	for _, u := range got.UserDefinedFields.UserDefinedField {
		udfNames[u.Name] = u.Value
	}
	for _, want := range requiredUDFs {
		if _, ok := udfNames[want]; !ok {
			t.Errorf("missing required UDF %q", want)
		}
	}

	// Spot-check substituted values — confirms options actually
	// reached the output.
	if udfNames["vaultdms.matter_number"] != "2026-PAT-014" {
		t.Errorf("matter_number UDF = %q", udfNames["vaultdms.matter_number"])
	}
	if udfNames["vaultdms.document_count"] != "2" {
		t.Errorf("document_count UDF = %q, want 2", udfNames["vaultdms.document_count"])
	}

	// Per-document Tags + Files structure — vendor ingest expects
	// every Document to have a Files block with at least one File of
	// type Native pointing at a path.
	for i, doc := range got.Batch.Documents.Document {
		if doc.DocID == "" {
			t.Errorf("doc[%d] has empty DocID attr", i)
		}
		if doc.MimeType == "" {
			t.Errorf("doc[%d] has empty MimeType attr", i)
		}
		if len(doc.Tags.Tag) == 0 {
			t.Errorf("doc[%d] has zero Tags", i)
		}
		// Required per-doc tags. Drop one of these and vendors
		// silently ingest with empty fields — operator gets a
		// useless export.
		needsTags := map[string]bool{
			"vaultdms.title":           false,
			"vaultdms.created_at":      false,
			"vaultdms.lifecycle_state": false,
			"vaultdms.version_id":      false,
			"vaultdms.content_sha256":  false,
		}
		for _, tag := range doc.Tags.Tag {
			if _, ok := needsTags[tag.TagName]; ok {
				needsTags[tag.TagName] = true
			}
		}
		for tagName, found := range needsTags {
			if !found {
				t.Errorf("doc[%d] missing required tag %q", i, tagName)
			}
		}
		if doc.Files == nil || len(doc.Files.File) == 0 {
			t.Errorf("doc[%d] has no Files block", i)
			continue
		}
		nativeFound := false
		for _, f := range doc.Files.File {
			if f.FileType == "Native" {
				if f.ExternalFile.FilePath == "" {
					t.Errorf("doc[%d] Native file has empty FilePath", i)
				}
				nativeFound = true
			}
		}
		if !nativeFound {
			t.Errorf("doc[%d] has no FileType=Native entry", i)
		}
	}
}

func TestRenderEDRM_EmptyManifestRejected(t *testing.T) {
	if _, err := RenderEDRM(nil, EDRMOptions{}); err == nil {
		t.Fatalf("nil manifest should error")
	}
}

func TestRenderEDRM_ZeroDocsStillStructural(t *testing.T) {
	// A manifest with zero documents should still produce a valid
	// EDRM skeleton — empty Batch/Documents, all UDFs present,
	// document_count = 0. Vendors that import a zero-doc matter
	// (initial intake before anyone uploads) need the shell.
	m := &DiscoveryManifest{
		Version:    "1",
		TenantID:   "00000000-0000-0000-0000-000000000001",
		ExportedAt: time.Now().UTC(),
		Documents:  nil,
	}
	xmlBytes, err := RenderEDRM(m, EDRMOptions{MatterNumber: "M-0", MatterName: "stub"})
	if err != nil {
		t.Fatalf("RenderEDRM: %v", err)
	}
	var got edrmRoot
	if err := xml.Unmarshal(xmlBytes, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Batch.Documents.Document) != 0 {
		t.Errorf("zero-doc manifest produced %d Document elements", len(got.Batch.Documents.Document))
	}
	// document_count UDF must still be present and equal "0".
	for _, u := range got.UserDefinedFields.UserDefinedField {
		if u.Name == "vaultdms.document_count" && u.Value != "0" {
			t.Errorf("document_count UDF = %q, want 0", u.Value)
		}
	}
}

func TestRenderEDRM_LegacyCaseFieldsRoundTrip(t *testing.T) {
	// When MatterNumber/MatterName are empty (legacy callers using
	// the old case_id/case_name shape), the manifest still carries
	// CaseID/CaseName; the EDRM serializer surfaces them as legacy
	// UDFs so old tooling that grepped them keeps working.
	m := sampleManifest()
	m.CaseID = "CASE-001"
	m.CaseName = "Pre-matters export"
	xmlBytes, err := RenderEDRM(m, EDRMOptions{}) // empty opts
	if err != nil {
		t.Fatalf("RenderEDRM: %v", err)
	}
	var got edrmRoot
	if err := xml.Unmarshal(xmlBytes, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	udf := map[string]string{}
	for _, u := range got.UserDefinedFields.UserDefinedField {
		udf[u.Name] = u.Value
	}
	if udf["vaultdms.case_id"] != "CASE-001" {
		t.Errorf("legacy case_id UDF = %q", udf["vaultdms.case_id"])
	}
	if udf["vaultdms.case_name"] != "Pre-matters export" {
		t.Errorf("legacy case_name UDF = %q", udf["vaultdms.case_name"])
	}
}

func TestRenderEDRM_EscapesUnsafeXMLContent(t *testing.T) {
	// Document titles can contain &, <, >, ", '. The serializer must
	// escape them properly via encoding/xml — passing a raw string
	// would break vendor parsers. encoding/xml does this for us;
	// the test guards against accidental hand-rolled XML synthesis
	// that bypasses the encoder.
	m := sampleManifest()
	m.Documents[0].Title = `Smith v. Jones <2025> & "exhibit A"`
	xmlBytes, err := RenderEDRM(m, EDRMOptions{MatterNumber: "M-1"})
	if err != nil {
		t.Fatalf("RenderEDRM: %v", err)
	}
	// Raw '&' should never appear in unescaped form — every '&' must
	// be the start of an entity reference.
	s := string(xmlBytes)
	for i := 0; i < len(s); i++ {
		if s[i] != '&' {
			continue
		}
		// Allowed: &amp; &lt; &gt; &quot; &apos; &#... Match the
		// minimum prefix; any other '&' is unescaped and a bug.
		rest := s[i+1:]
		if !(strings.HasPrefix(rest, "amp;") ||
			strings.HasPrefix(rest, "lt;") ||
			strings.HasPrefix(rest, "gt;") ||
			strings.HasPrefix(rest, "quot;") ||
			strings.HasPrefix(rest, "apos;") ||
			strings.HasPrefix(rest, "#")) {
			t.Errorf("unescaped '&' at byte %d: %q", i, s[max(0, i-10):min(len(s), i+10)])
		}
	}

	// Round-trip — a vendor parser that respects escapes should
	// recover the original title bytes.
	var got edrmRoot
	if err := xml.Unmarshal(xmlBytes, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, doc := range got.Batch.Documents.Document {
		if doc.DocID != m.Documents[0].DocumentID {
			continue
		}
		for _, tag := range doc.Tags.Tag {
			if tag.TagName == "vaultdms.title" {
				if tag.TagValue != m.Documents[0].Title {
					t.Errorf("title round-trip drift: got %q want %q",
						tag.TagValue, m.Documents[0].Title)
				}
			}
		}
	}
}

// sampleManifest is the shared test fixture. Two docs, one with a
// version and one without, so we exercise both branches.
func sampleManifest() *DiscoveryManifest {
	return &DiscoveryManifest{
		Version:        "1",
		CaseID:         "C-1",
		CaseName:       "fixture",
		CustodianEmail: "alice@acme.test",
		TenantID:       "00000000-0000-0000-0000-000000000001",
		ExportedBy:     "00000000-0000-0000-0000-000000000002",
		ExportedAt:     time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC),
		Documents: []DiscoveryManifestItem{
			{
				DocumentID:     "11111111-1111-1111-1111-111111111111",
				Title:          "Patent application draft",
				LifecycleState: "active",
				CurrentVersion: "22222222-2222-2222-2222-222222222222",
				ContentSHA256:  "abc123",
				CreatedAt:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			},
			{
				DocumentID:     "33333333-3333-3333-3333-333333333333",
				Title:          "Email — Smith → Jones, 2026-02-14",
				LifecycleState: "retained",
				ContentSHA256:  "def456",
				CreatedAt:      time.Date(2026, 2, 14, 9, 0, 0, 0, time.UTC),
			},
		},
	}
}

