package service

import (
	"sort"
	"strings"
)

// ReservedMetadataPrefix marks custom_metadata keys only the server writes
// (D-25). Every user-facing metadata write refuses them, so a key in this
// namespace is a trustworthy system marker — e.g. the orphan GC's
// exemption for documents that are metadata-only by design.
const ReservedMetadataPrefix = "_sedoc."

// SystemSourceKey records which server-side ingest created a document;
// SystemSourceM365 is the Outlook add-in ingest.
const (
	SystemSourceKey  = ReservedMetadataPrefix + "source"
	SystemSourceM365 = "m365"
)

// rejectReservedMetadataKeys refuses user-supplied metadata that names a
// reserved key.
func rejectReservedMetadataKeys(meta map[string]any) error {
	var bad []string
	for k := range meta {
		if strings.HasPrefix(k, ReservedMetadataPrefix) {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return errInvalidInput("custom_metadata",
		"keys beginning with "+ReservedMetadataPrefix+" are reserved for the system: "+strings.Join(bad, ", "))
}

// withReservedFrom returns meta plus the reserved keys of prev, so a user
// replace of custom_metadata never drops a system marker.
func withReservedFrom(meta, prev map[string]any) map[string]any {
	out := make(map[string]any, len(meta))
	for k, v := range meta {
		out[k] = v
	}
	for k, v := range prev {
		if strings.HasPrefix(k, ReservedMetadataPrefix) {
			out[k] = v
		}
	}
	return out
}
