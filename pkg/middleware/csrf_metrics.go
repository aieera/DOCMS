package middleware

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// csrfRejectionsTotal counts CSRF double-submit rejections, labelled by
// the reason (missing cookie / missing header / token mismatch). High
// rates of "missing cookie" usually mean a frontend bug; "token
// mismatch" rates rising in isolation are the actual attack signal.
var csrfRejectionsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "csrf_rejections_total",
		Help: "CSRF double-submit middleware rejections, labelled by reason.",
	},
	[]string{"reason"},
)
