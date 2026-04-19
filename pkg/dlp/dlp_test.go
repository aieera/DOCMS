package dlp

import (
	"context"
	"testing"
)

func TestDefaultRules_DetectCreditCard(t *testing.T) {
	s := NewScanner(nil)
	result := s.Scan(context.Background(), "Pay with 4111-1111-1111-1111 please")
	if len(result.Matches) == 0 {
		t.Fatal("expected credit card match")
	}
	if !result.ShouldBlock {
		t.Fatal("credit card should trigger block")
	}
}

func TestDefaultRules_DetectSSN(t *testing.T) {
	s := NewScanner(nil)
	result := s.Scan(context.Background(), "SSN is 123-45-6789")
	if len(result.Matches) == 0 {
		t.Fatal("expected SSN match")
	}
}

func TestCustomKeywords(t *testing.T) {
	s := NewScanner(nil)
	s.AddCustomKeywords([]string{"confidential", "top secret"}, ActionBlock)
	result := s.Scan(context.Background(), "This document is CONFIDENTIAL")
	if !result.ShouldBlock {
		t.Fatal("custom keyword should trigger block")
	}
}

func TestCleanText(t *testing.T) {
	s := NewScanner(nil)
	result := s.Scan(context.Background(), "This is a normal business document about quarterly earnings.")
	if result.ShouldBlock || result.ShouldWarn {
		t.Fatal("clean text should not trigger")
	}
}

func TestMaskValue(t *testing.T) {
	m := maskValue("4111111111111111")
	if m == "4111111111111111" {
		t.Fatal("value should be masked")
	}
	if m[:4] != "4111" {
		t.Fatalf("first 4 should be visible, got %s", m[:4])
	}
}
