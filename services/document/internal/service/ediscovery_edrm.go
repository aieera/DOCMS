// EDRM v1.2 manifest serializer (ADR 0038).
//
// Renders the same DiscoveryManifest source-of-truth into EDRM XML
// alongside the existing manifest.json. Discovery vendors that grep
// our JSON keep working; vendors that need EDRM (most of them) get
// it without operators hand-translating.
//
// Conformance is minimum-viable LoadFile (v1.2) — ingest paths for
// Relativity, Concordance, and Nuix accept this shape. Per-vendor
// extensions live in <UserDefinedField> per the v1.2 spec §3.4.
//
// Template lives at docs/ediscovery/edrm-schema.xml as the
// human-readable artifact vendors grep before connecting; the Go
// path here is independent of the template (encoding/xml is more
// reliable than text substitution for nested values that need
// escaping). The template stays authoritative for the *shape*; if
// the two drift, the e2e tests catch it (follow-up PR).

package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
	"time"
)

// EDRM XML element types. Field names mirror the EDRM spec verbatim
// so xml struct tags read the same way the spec does. Pointer types
// where the field is genuinely optional (per spec) so xml.Marshal
// omits the element rather than emitting an empty one.

type edrmRoot struct {
	XMLName             xml.Name `xml:"Root"`
	DataInterchangeType string   `xml:"DataInterchangeType,attr"`
	MajorVersion        string   `xml:"MajorVersion,attr"`
	MinorVersion        string   `xml:"MinorVersion,attr"`
	Description         string   `xml:"Description,attr"`
	XMLNS               string   `xml:"xmlns:xsi,attr"`

	Batch              edrmBatch              `xml:"Batch"`
	Relationships      edrmRelationships      `xml:"Relationships"`
	UserDefinedFields  edrmUserDefinedFields  `xml:"UserDefinedFields"`
}

type edrmBatch struct {
	Documents edrmDocuments `xml:"Documents"`
}

type edrmDocuments struct {
	Document []edrmDocument `xml:"Document"`
}

type edrmDocument struct {
	DocID    string     `xml:"DocID,attr"`
	MimeType string     `xml:"MimeType,attr"`
	Tags     edrmTags   `xml:"Tags"`
	Files    *edrmFiles `xml:"Files,omitempty"`
}

type edrmTags struct {
	Tag []edrmTag `xml:"Tag"`
}

type edrmTag struct {
	TagName     string `xml:"TagName,attr"`
	TagDataType string `xml:"TagDataType,attr"`
	TagValue    string `xml:"TagValue,attr"`
}

type edrmFiles struct {
	File []edrmFile `xml:"File"`
}

type edrmFile struct {
	FileType     string           `xml:"FileType,attr"`
	ExternalFile edrmExternalFile `xml:"ExternalFile"`
}

type edrmExternalFile struct {
	FilePath string `xml:"FilePath,attr"`
}

type edrmRelationships struct {
	// Empty in v1; vendors expect the element to exist even when empty.
	// xml.Marshal renders <Relationships></Relationships> for an
	// empty struct, which is the spec-compliant form.
}

type edrmUserDefinedFields struct {
	UserDefinedField []edrmUserDefinedField `xml:"UserDefinedField"`
}

type edrmUserDefinedField struct {
	Name     string `xml:"Name,attr"`
	DataType string `xml:"DataType,attr"`
	Value    string `xml:",chardata"`
}

// EDRMOptions carries the matter-level metadata that EDRM XML needs
// but the existing JSON DiscoveryManifest doesn't yet (it pre-dates
// the matters table). The serializer accepts these as a separate
// struct so the JSON manifest format is unchanged.
//
// MatterNumber + MatterName + ExportID come from ediscovery_matters
// + ediscovery_exports rows. CustodianEmail comes from the legacy
// request body (still accepted) — when the matter has multiple
// custodians, callers pass the primary; the rest land in the
// ediscovery_custodians table for the matter detail page.
type EDRMOptions struct {
	MatterNumber   string
	MatterName     string
	ExportID       string
	CustodianEmail string
}

// RenderEDRM writes the EDRM v1.2 XML for a DiscoveryManifest.
// Returns the canonical bytes; callers wrap them in a zip entry
// next to the existing manifest.json.
func RenderEDRM(m *DiscoveryManifest, opts EDRMOptions) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("RenderEDRM: nil manifest")
	}

	root := edrmRoot{
		DataInterchangeType: "LoadFile",
		MajorVersion:        "1",
		MinorVersion:        "2",
		Description:         "VaultDMS eDiscovery export",
		XMLNS:               "http://www.w3.org/2001/XMLSchema-instance",
		UserDefinedFields: edrmUserDefinedFields{
			UserDefinedField: []edrmUserDefinedField{
				{Name: "vaultdms.tenant_id", DataType: "Text", Value: m.TenantID},
				{Name: "vaultdms.matter_number", DataType: "Text", Value: opts.MatterNumber},
				{Name: "vaultdms.matter_name", DataType: "Text", Value: opts.MatterName},
				{Name: "vaultdms.export_id", DataType: "Text", Value: opts.ExportID},
				{Name: "vaultdms.exported_at", DataType: "DateTime", Value: m.ExportedAt.Format(time.RFC3339)},
				{Name: "vaultdms.custodian_email", DataType: "Text", Value: opts.CustodianEmail},
				{Name: "vaultdms.exported_by", DataType: "Text", Value: m.ExportedBy},
				{Name: "vaultdms.document_count", DataType: "Integer", Value: strconv.Itoa(len(m.Documents))},
				// Legacy free-text fields that pre-date matters. Kept
				// so existing tooling that reads case_id/case_name
				// from the JSON manifest can also find them in EDRM.
				{Name: "vaultdms.case_id", DataType: "Text", Value: m.CaseID},
				{Name: "vaultdms.case_name", DataType: "Text", Value: m.CaseName},
			},
		},
	}

	docs := make([]edrmDocument, 0, len(m.Documents))
	for _, it := range m.Documents {
		// Per-doc native file path inside the bundle ZIP. The current
		// flow does NOT include native blobs in the ZIP (chain-of-
		// custody is metadata-only — see ediscovery.go header). When
		// the async upload follow-up lands, this path will point at
		// the ZIP entry; for now it points at the doc-<uuid>.json
		// metadata blob the existing flow already writes, so the
		// EDRM is internally consistent.
		nativePath := "doc-" + it.DocumentID + ".json"
		doc := edrmDocument{
			DocID:    it.DocumentID,
			MimeType: "application/octet-stream",
			Tags: edrmTags{
				Tag: []edrmTag{
					{TagName: "vaultdms.title", TagDataType: "Text", TagValue: it.Title},
					{TagName: "vaultdms.created_at", TagDataType: "DateTime", TagValue: it.CreatedAt.Format(time.RFC3339)},
					{TagName: "vaultdms.lifecycle_state", TagDataType: "Text", TagValue: it.LifecycleState},
					{TagName: "vaultdms.version_id", TagDataType: "Text", TagValue: it.CurrentVersion},
					{TagName: "vaultdms.content_sha256", TagDataType: "Text", TagValue: it.ContentSHA256},
				},
			},
			Files: &edrmFiles{
				File: []edrmFile{{
					FileType:     "Native",
					ExternalFile: edrmExternalFile{FilePath: nativePath},
				}},
			},
		}
		docs = append(docs, doc)
	}
	root.Batch.Documents.Document = docs

	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("encode EDRM: %w", err)
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

