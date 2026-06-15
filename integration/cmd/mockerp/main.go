// Command mockerp simulates the ERP boundary [B] so the integration runs
// end-to-end without a real ERP:
//
//	mockerp serve                 # render + authz endpoints (for worker/BFF)
//	mockerp emit <kind> [flags]   # POST a canonical event to the worker webhook
//
// kinds: customer.created customer.updated document.committed attachment.uploaded
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

	"github.com/google/uuid"

	"github.com/aieera/sedoc/integration/internal/erp"
)

// minimalPDF is a tiny syntactically-valid PDF used as sample document bytes.
var minimalPDF = []byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
	"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
	"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]>>endobj\n" +
	"trailer<</Root 1 0 R>>\n%%EOF\n")

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "serve":
		serve()
	case "emit":
		emit(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mockerp serve | mockerp emit <kind> [flags]")
	os.Exit(2)
}

// serve runs the render + authz endpoints a worker/BFF call.
func serve() {
	addr := envOr("ERP_LISTEN_ADDR", ":8095")
	mux := http.NewServeMux()
	// Render: any file_ref → the sample PDF.
	mux.HandleFunc("GET /erp/documents/{id}/pdf", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(minimalPDF)
	})
	// Authz: allow everyone except a user id prefixed "noauth-" (demo of denial).
	mux.HandleFunc("GET /erp/authz/customer/{ref}", func(w http.ResponseWriter, r *http.Request) {
		user := r.URL.Query().Get("user")
		if len(user) >= 7 && user[:7] == "noauth-" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	fmt.Fprintf(os.Stderr, "mock ERP listening on %s\n", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// emit POSTs one canonical event to the worker webhook.
func emit(args []string) {
	if len(args) < 1 {
		usage()
	}
	kind := args[0]
	fs := flag.NewFlagSet("emit", flag.ExitOnError)
	webhook := fs.String("webhook", envOr("ERP_WEBHOOK_URL", "http://localhost:8090/webhooks/erp"), "worker webhook URL")
	eventID := fs.String("event-id", uuid.NewString(), "stable erp_event_id (re-use to test dedup)")
	customer := fs.String("customer", "", "customer_ref")
	name := fs.String("name", "", "customer name")
	docType := fs.String("doc-type", "invoice", "quote|po|so|do|invoice")
	docNumber := fs.String("doc-number", "", "ERP document number")
	status := fs.String("status", "confirmed", "ERP status")
	filename := fs.String("filename", "", "attachment filename")
	inline := fs.Bool("inline", true, "send sample PDF bytes inline (else a file_ref the render endpoint serves)")
	_ = fs.Parse(args[1:])

	e := erp.Event{ERPEventID: *eventID, Kind: kind, CustomerRef: *customer, Name: *name}
	switch kind {
	case erp.KindCustomerCreated, erp.KindCustomerUpdated:
		// no payload beyond customer + name
	case erp.KindDocumentCommitted:
		e.DocType, e.DocNumber, e.Status = *docType, *docNumber, *status
		e.Mime = "application/pdf"
		attachBytes(&e, *inline, *docType+"-"+*docNumber+".pdf")
	case erp.KindAttachmentUploaded:
		e.DocType = *docType
		e.Mime = "application/pdf"
		fn := *filename
		if fn == "" {
			fn = "attachment.pdf"
		}
		attachBytes(&e, *inline, fn)
	default:
		usage()
	}

	body, _ := json.Marshal(e)
	resp, err := http.Post(*webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, "post:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	fmt.Printf("%d %s\n", resp.StatusCode, string(out))
}

func attachBytes(e *erp.Event, inline bool, filename string) {
	e.Filename = filename
	if inline {
		e.Bytes = minimalPDF
	} else {
		e.FileRef = "file-" + uuid.NewString()
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
