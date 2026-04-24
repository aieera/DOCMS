package main

import (
	"strings"
	"testing"
)

func TestKeyFingerprint_NotPrefixOfSecret(t *testing.T) {
	secrets := []string{
		"a1b2c3d4e5f6071829384756abcdef0123456789abcdef0123456789abcdef01",
		"deadbeefcafebabefeedface00112233",
		"short",
	}
	for _, s := range secrets {
		fp := keyFingerprint(s)
		if fp == "" {
			t.Fatalf("empty fingerprint for %q", s)
		}
		if len(s) >= len(fp) && strings.HasPrefix(s, fp) {
			t.Fatalf("fingerprint %q is a prefix of secret %q — leaks key material", fp, s)
		}
	}
}

func TestKeyFingerprint_Deterministic(t *testing.T) {
	s := "deadbeefcafebabefeedface00112233"
	if keyFingerprint(s) != keyFingerprint(s) {
		t.Fatal("fingerprint not deterministic")
	}
}
