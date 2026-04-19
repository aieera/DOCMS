package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// natsCheck verifies that every subject passed on argv is covered by at
// least one provisioned JetStream stream's subject filter. Exits 0 when
// all subjects are covered, exits 1 otherwise. Designed for preflight
// use from shell scripts.
//
// Usage:
//
//	dms-admin nats check dms.audit.> dms.document.> ...
//	dms-admin nats check --service=audit dms.audit.>
//
// With --json, a machine-readable report is written to stdout.
func natsCheck(args []string) {
	var (
		service  string
		jsonOut  bool
		subjects []string
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			jsonOut = true
		case strings.HasPrefix(a, "--service="):
			service = strings.TrimPrefix(a, "--service=")
		case a == "--service" && i+1 < len(args):
			i++
			service = args[i]
		case strings.HasPrefix(a, "--"):
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n", a)
			os.Exit(2)
		default:
			subjects = append(subjects, a)
		}
	}
	if len(subjects) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: dms-admin nats check [--service=NAME] [--json] SUBJECT [SUBJECT...]")
		os.Exit(2)
	}

	js, nc := mustConnectJetStream()
	defer nc.Close()

	var streamSubjects []streamSubject
	for name := range js.StreamNames() {
		info, err := js.StreamInfo(name)
		if err != nil {
			continue
		}
		for _, sub := range info.Config.Subjects {
			streamSubjects = append(streamSubjects, streamSubject{Stream: name, Subject: sub})
		}
	}

	type result struct {
		Subject    string `json:"subject"`
		Service    string `json:"service,omitempty"`
		Covered    bool   `json:"covered"`
		CoveredBy  string `json:"covered_by,omitempty"`
		StreamName string `json:"stream,omitempty"`
	}
	var results []result
	allOK := true
	for _, s := range subjects {
		covered := false
		var coverStream, coverSubj string
		for _, ss := range streamSubjects {
			if subjectCovers(ss.Subject, s) {
				covered = true
				coverStream = ss.Stream
				coverSubj = ss.Subject
				break
			}
		}
		results = append(results, result{
			Subject: s, Service: service, Covered: covered,
			CoveredBy: coverSubj, StreamName: coverStream,
		})
		if !covered {
			allOK = false
		}
	}

	if jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"ok":      allOK,
			"results": results,
		})
	} else {
		for _, r := range results {
			if r.Covered {
				fmt.Printf("OK   %-30s  covered by %s[%s]\n", r.Subject, r.StreamName, r.CoveredBy)
			} else {
				prefix := ""
				if r.Service != "" {
					prefix = " (required by " + r.Service + ")"
				}
				fmt.Printf("MISS %-30s  no stream covers this subject%s\n", r.Subject, prefix)
			}
		}
	}
	if !allOK {
		os.Exit(1)
	}
}

type streamSubject struct{ Stream, Subject string }

// subjectCovers reports whether NATS subject filter `filter` covers every
// message that would match subscribe subject `subj`. Implements the subset
// of NATS subject matching needed for preflight: literal tokens, `*`
// single-token wildcard, and `>` tail wildcard. A filter covers a subject
// iff every concrete message matching `subj` also matches `filter`.
func subjectCovers(filter, subj string) bool {
	ft := strings.Split(filter, ".")
	st := strings.Split(subj, ".")
	for i := 0; i < len(ft); i++ {
		if ft[i] == ">" {
			return i < len(st) || len(ft) == len(st)+1 && i == len(st)
		}
		if i >= len(st) {
			return false
		}
		switch {
		case ft[i] == "*":
			if st[i] == ">" {
				return false
			}
		case ft[i] == st[i]:
		default:
			return false
		}
	}
	return len(ft) == len(st)
}
