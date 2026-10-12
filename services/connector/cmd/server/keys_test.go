package main

import (
	"bytes"
	"testing"

	"github.com/aieera/sedoc/pkg/config"
)

// N-10: the connector's sealing key and OAuth-state HMAC are derived from
// SEDOC_LOCAL_KEK. With an external KMS (vault/aws) the platform config does
// not require that variable, and the keys used to be derived from the bare
// domain prefix — the same, public value on every install.
func TestConnectorStartup_RequiresKEK(t *testing.T) {
	for _, c := range []*config.Config{
		{Environment: "prod", KMSProvider: "vault"},
		{Environment: "prod", KMSProvider: "aws"},
		{Environment: "prod", KMSProvider: "local"},
		{Environment: "dev", KMSProvider: "local"},
	} {
		if _, _, err := connectorKeys(c); err == nil {
			t.Errorf("env=%s kms=%s: connector keys derived without SEDOC_LOCAL_KEK", c.Environment, c.KMSProvider)
		}
	}
	seal, mac, err := connectorKeys(&config.Config{Environment: "prod", KMSProvider: "vault", LocalKEK: "a-real-kek"})
	if err != nil {
		t.Fatal(err)
	}
	if len(seal) != 32 || len(mac) != 32 || bytes.Equal(seal, mac) {
		t.Fatalf("want two distinct 32-byte keys")
	}
	other, _, _ := connectorKeys(&config.Config{LocalKEK: "another-kek"})
	if bytes.Equal(seal, other) {
		t.Fatal("keys must depend on the KEK")
	}
}
