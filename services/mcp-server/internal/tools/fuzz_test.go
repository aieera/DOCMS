package tools_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/services/mcp-server/internal/tools"
)

// FuzzToolArguments hammers every tool's argument parser with arbitrary
// bytes. An LLM (or a malicious client impersonating one) controls the
// `arguments` blob verbatim, so no input may panic the server or slip past
// validation into a malformed upstream call. The invariant checked for
// every tool + input: the handler returns without panicking, and a nil
// error always comes with a non-nil result (never a silent empty success).
//
// Run in CI with a short budget:
//
//	go test -run=^$ -fuzz=FuzzToolArguments -fuzztime=20s ./internal/tools/
//
// The seed corpus also executes under a plain `go test`, so the target is
// gated on every PR even without the -fuzz flag.
func FuzzToolArguments(f *testing.F) {
	base, _ := fakeUpstream(f, 200, `{"ok":true}`)
	defs := tools.All(cfgFor(base))
	ctx := tenantCtx(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))

	// Seeds: valid shapes, partial shapes, and malformed JSON.
	f.Add([]byte(`{"query":"hello","limit":5}`))
	f.Add([]byte(`{"document_id":"` + uuid.NewString() + `"}`))
	f.Add([]byte(`{"workspace_id":"w","title":"t","content":"c"}`))
	f.Add([]byte(`{"definition_id":"d","document_id":"x"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"limit":-2147483648}`))
	f.Add([]byte(`{"query":`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))
	f.Add([]byte(`{"content":"` + string(make([]byte, 1024)) + `"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, d := range defs {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("tool %q panicked on arguments %q: %v", d.Name, data, r)
					}
				}()
				res, err := d.Handler(ctx, json.RawMessage(data))
				if err == nil && res == nil {
					t.Fatalf("tool %q returned (nil, nil) for arguments %q — a success must carry a result", d.Name, data)
				}
			}()
		}
	})
}
