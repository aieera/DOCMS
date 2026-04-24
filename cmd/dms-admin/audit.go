// dms-admin audit — operator CLI for audit service.
//
// Subcommands:
//   audit verify --tenant <uuid> [--since <ISO-8601>] [--audit-url <url>]
//
// Exit codes: 0 when the chain is valid, 1 when a break is detected,
// 2 for transport / setup errors. The non-zero-break exit is how the
// caller can branch in CI (e.g. a nightly integrity check that pages
// on-call on exit 1).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func auditMain(args []string) {
	if len(args) == 0 {
		fmt.Println("Subcommands: verify")
		return
	}
	switch args[0] {
	case "verify":
		auditVerify(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown audit subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func auditVerify(args []string) {
	fs := flag.NewFlagSet("audit verify", flag.ExitOnError)
	tenant := fs.String("tenant", "", "tenant uuid (required)")
	since := fs.String("since", "", "optional lower-bound timestamp (ISO-8601); passed to the service as from_ts")
	base := fs.String("audit-url", envOr("AUDIT_SERVICE_URL", "http://localhost:8080"), "audit service base URL")
	_ = fs.Parse(args)
	if *tenant == "" {
		fmt.Fprintln(os.Stderr, "--tenant is required")
		os.Exit(2)
	}

	body := map[string]string{"tenant_id": *tenant}
	if *since != "" {
		// Validate the timestamp here so a typo exits before the round-trip.
		if _, err := time.Parse(time.RFC3339, *since); err != nil {
			fmt.Fprintf(os.Stderr, "--since must be RFC3339 (e.g. 2024-01-01T00:00:00Z): %v\n", err)
			os.Exit(2)
		}
		body["from_ts"] = *since
	}
	payload, _ := json.Marshal(body)

	req, err := http.NewRequest(http.MethodPost, *base+"/api/v1/audit/verify-integrity", bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "build request: %v\n", err)
		os.Exit(2)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", *tenant)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit call: %v\n", err)
		os.Exit(2)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		fmt.Fprintf(os.Stderr, "audit returned HTTP %d: %s\n", resp.StatusCode, string(raw))
		os.Exit(2)
	}

	var result struct {
		TenantID     string `json:"tenant_id"`
		Valid        bool   `json:"valid"`
		TotalEvents  int64  `json:"total_events"`
		Verified     int64  `json:"verified"`
		BrokenAt     string `json:"broken_at,omitempty"`
		BrokenHash   string `json:"broken_hash,omitempty"`
		ExpectedHash string `json:"expected_hash,omitempty"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		fmt.Fprintf(os.Stderr, "parse response: %v\n%s\n", err, string(raw))
		os.Exit(2)
	}

	if result.Valid {
		fmt.Printf("ok: tenant=%s verified=%d/%d\n", result.TenantID, result.Verified, result.TotalEvents)
		return
	}
	fmt.Println("CHAIN BROKEN")
	fmt.Printf("  tenant=%s\n", result.TenantID)
	fmt.Printf("  verified_before_break=%d\n", result.Verified)
	fmt.Printf("  total_events=%d\n", result.TotalEvents)
	fmt.Printf("  broken_at_event_id=%s\n", result.BrokenAt)
	fmt.Printf("  expected_hash=%s\n", result.ExpectedHash)
	fmt.Printf("  actual_hash=%s\n", result.BrokenHash)
	fmt.Println("(dms.audit.tamper_detected.v1 emitted by the service — check NATS AUDIT_EVENTS stream)")
	os.Exit(1)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
