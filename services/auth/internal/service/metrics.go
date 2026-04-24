package service

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Wave 15.3 password-lifecycle metrics. Registered on the default
// registry at package init; scraped from the shared /metrics
// endpoint wired in cmd/server/main.go.
//
// PasswordChangedTotal counts completed password changes, labelled
// by reason (admin_reset, expiry, first_login, self, hibp_compromise)
// so operators can distinguish forced vs. voluntary churn.
var PasswordChangedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "auth_password_changed_total",
		Help: "Successful password changes, labelled by reason.",
	},
	[]string{"reason"},
)

// PasswordExpiriesPending is a gauge-per-tenant of how many users
// the sweeper has just flagged as needing a change. Adds to the
// overall pending backlog; operators alert on the sum breaching a
// threshold via ops/prometheus/rules/wave-15.yml.
var PasswordExpiriesPending = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "auth_password_expiries_pending",
		Help: "Users flagged by the password-expiry sweeper, by tenant.",
	},
	[]string{"tenant"},
)
