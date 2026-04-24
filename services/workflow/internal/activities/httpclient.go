package activities

// httpclient.go — T-D-5. Every HTTP call an activity makes must go
// through `httpClient`, NOT `http.DefaultClient`.
//
// Problems with http.DefaultClient:
//   - no Timeout → under slow-downstream a goroutine + socket is
//     pinned until the activity's workflow-level StartToCloseTimeout
//     fires (minutes), wasting the worker's tiny goroutine budget.
//   - no otelhttp transport → activity spans don't propagate the
//     trace context outwards, breaking cross-service tracing.
//
// Lint enforcement: .golangci.yml has a custom rule blocking
// http.DefaultClient references under services/workflow/. Adding a
// new call site should fail the lint job.

import (
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// httpClient is the sole outbound HTTP client for activities.
// 10-second per-request timeout; activity-level retries come from
// Temporal's RetryPolicy (see workflows/wave15.go).
var httpClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: otelhttp.NewTransport(http.DefaultTransport),
}
