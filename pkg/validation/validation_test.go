package validation

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsValidUUID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"00000000-0000-0000-0000-000000000000", true},
		{"aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", true},
		{"019d99f0-c34a-7c0b-badf-aa3f7a14f08b", true}, // v7
		{"", false},
		{"not-a-uuid", false},
		{"12345678", false},
		{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaz", false},     // bad hex
		{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaaa", false},    // too long
	}
	for _, tc := range cases {
		if got := IsValidUUID(tc.in); got != tc.want {
			t.Errorf("IsValidUUID(%q): got %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestContainsNullByte(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"hello", false},
		{"", false},
		{"hello\x00world", true},
		{"\x00", true},
		{"trailing\x00", true},
	}
	for _, tc := range cases {
		if got := ContainsNullByte(tc.in); got != tc.want {
			t.Errorf("ContainsNullByte(%q): got %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestTrimString(t *testing.T) {
	cases := []struct {
		in     string
		maxLen int
		want   string
	}{
		{"  hello  ", 0, "hello"},
		{"hello", 3, "hel"},
		{"hello", 100, "hello"},
		{"  café  ", 3, "caf"}, // rune-count trim, not byte-count
		{"", 10, ""},
	}
	for _, tc := range cases {
		if got := TrimString(tc.in, tc.maxLen); got != tc.want {
			t.Errorf("TrimString(%q, %d): got %q, want %q", tc.in, tc.maxLen, got, tc.want)
		}
	}
}

func TestCheckJSONDepth_Limits(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"flat object", `{"a":1}`, false},
		{"nested 3 levels", `{"a":{"b":{"c":1}}}`, false},
		{"exactly max", strings.Repeat("{", MaxJSONDepth) + strings.Repeat("}", MaxJSONDepth), false},
		{"over max", strings.Repeat("{", MaxJSONDepth+1) + strings.Repeat("}", MaxJSONDepth+1), true},
		{"deep array", strings.Repeat("[", MaxJSONDepth+1) + strings.Repeat("]", MaxJSONDepth+1), true},
		{"empty", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkJSONDepth([]byte(tc.body), MaxJSONDepth)
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// ---- DecodeAndValidate -----------------------------------------------------

type payload struct {
	Name string `json:"name" validate:"required,safetxt,min=1,max=50"`
	ID   string `json:"id"   validate:"required,uuid"`
}

func postWithBody(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/x", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	// Run DecodeAndValidate against the request — use recorder as sink.
	return httptest.NewRecorder()
}

func TestDecodeAndValidate_HappyPath(t *testing.T) {
	body := `{"name":"Alice","id":"aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"}`
	req := httptest.NewRequest("POST", "/x", bytes.NewBufferString(body))
	var p payload
	if err := DecodeAndValidate(req, &p); err != nil {
		t.Fatalf("valid body rejected: %v", err)
	}
	if p.Name != "Alice" {
		t.Errorf("name: %q", p.Name)
	}
}

func TestDecodeAndValidate_RejectsNullBytes(t *testing.T) {
	body := "{\"name\":\"Ali\x00ce\",\"id\":\"aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa\"}"
	req := httptest.NewRequest("POST", "/x", bytes.NewBufferString(body))
	var p payload
	err := DecodeAndValidate(req, &p)
	if err == nil {
		t.Fatal("null byte must be rejected")
	}
	if !strings.Contains(err.Error(), "null byte") {
		t.Errorf("error message: %v", err)
	}
}

func TestDecodeAndValidate_RejectsDeepJSON(t *testing.T) {
	deep := strings.Repeat("{\"a\":", MaxJSONDepth+1) + "1" + strings.Repeat("}", MaxJSONDepth+1)
	req := httptest.NewRequest("POST", "/x", bytes.NewBufferString(deep))
	var p payload
	err := DecodeAndValidate(req, &p)
	if err == nil {
		t.Fatal("overly deep JSON must be rejected")
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Errorf("depth error expected, got: %v", err)
	}
}

func TestDecodeAndValidate_RejectsMalformedJSON(t *testing.T) {
	req := httptest.NewRequest("POST", "/x", bytes.NewBufferString("{{{"))
	var p payload
	err := DecodeAndValidate(req, &p)
	if err == nil {
		t.Fatal("malformed JSON must be rejected")
	}
}

func TestDecodeAndValidate_RejectsInvalidUUIDField(t *testing.T) {
	body := `{"name":"Alice","id":"not-a-uuid"}`
	req := httptest.NewRequest("POST", "/x", bytes.NewBufferString(body))
	var p payload
	err := DecodeAndValidate(req, &p)
	if err == nil {
		t.Fatal("invalid uuid must be rejected via the 'uuid' tag")
	}
}

func TestStruct_RejectsMissingRequired(t *testing.T) {
	p := payload{} // both fields empty
	err := Struct(&p)
	if err == nil {
		t.Fatal("missing required fields must fail")
	}
	// Error should mention the field names.
	if !strings.Contains(err.Error(), "Name") || !strings.Contains(err.Error(), "ID") {
		t.Errorf("error should list violated fields, got: %v", err)
	}
}

func TestStruct_PassesValidPayload(t *testing.T) {
	p := payload{Name: "ok", ID: "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"}
	if err := Struct(&p); err != nil {
		t.Errorf("valid payload rejected: %v", err)
	}
}
