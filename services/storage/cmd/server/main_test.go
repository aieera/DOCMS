package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// H-20 / DEC-10: storage used to WARN and carry on without envelope
// encryption whenever the KEK was missing or invalid, so a deployment
// that lost its KEK secret silently stored plaintext. It now refuses to
// start without a KeyManager unless SEDOC_STORAGE_ENCRYPT_AT_REST=false
// is set explicitly.
func TestStorageStartup_RequiresKeyManagerUnlessExplicitlyDisabled(t *testing.T) {
	validKEK := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	var warnings []string
	warn := func(msg string) { warnings = append(warnings, msg) }

	cases := []struct {
		name        string
		kek, env    string
		wantErr     string
		wantKM      bool
		wantEncrypt bool
		wantWarning string
	}{
		{name: "no KEK, flag unset refuses", kek: "", env: "", wantErr: "SEDOC_LOCAL_KEK"},
		{name: "no KEK, flag true refuses", kek: "", env: "true", wantErr: "SEDOC_LOCAL_KEK"},
		{name: "invalid KEK, flag unset refuses", kek: "not-base64!", env: "", wantErr: "SEDOC_LOCAL_KEK"},
		{name: "unparseable flag refuses", kek: validKEK, env: "maybe", wantErr: "SEDOC_STORAGE_ENCRYPT_AT_REST"},
		{name: "no KEK, explicit false starts unencrypted", kek: "", env: "false",
			wantEncrypt: false, wantWarning: "disabled"},
		{name: "no KEK, explicit 0 starts unencrypted", kek: "", env: " 0 ",
			wantEncrypt: false, wantWarning: "disabled"},
		{name: "invalid KEK, explicit false starts unencrypted", kek: "not-base64!", env: "FALSE",
			wantEncrypt: false, wantWarning: "invalid"},
		{name: "valid KEK, flag unset encrypts", kek: validKEK, env: "",
			wantKM: true, wantEncrypt: true},
		{name: "valid KEK, flag true encrypts", kek: validKEK, env: "yes",
			wantKM: true, wantEncrypt: true},
		// A wired KeyManager keeps encryption on (service.New has always
		// forced it); the ignored flag is now said out loud at boot.
		{name: "valid KEK, explicit false keeps encrypting and says so", kek: validKEK, env: "false",
			wantKM: true, wantEncrypt: true, wantWarning: "ignored"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warnings = nil
			posture, err := resolveEncryption(tc.kek, tc.env, warn)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantKM, posture.KM != nil)
			require.Equal(t, tc.wantEncrypt, posture.EncryptAtRest)
			if tc.wantWarning != "" {
				require.True(t, strings.Contains(strings.Join(warnings, "\n"), tc.wantWarning),
					"expected a boot warning containing %q, got %q", tc.wantWarning, warnings)
			}
		})
	}
}
