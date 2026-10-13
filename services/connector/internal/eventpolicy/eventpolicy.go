// Package eventpolicy is the single allow-list of platform events that may
// leave the platform through a tenant-configured channel: webhook
// subscriptions, webhook fan-out, and the per-tenant event-stream mirror
// (DEC-16).
//
// Everything not on the list stays internal. In particular dms.auth.*,
// dms.session.*, dms.apikey.* and dms.user.* carry credentials or identity
// material (the password-reset event once carried the plaintext reset
// token, N-1), so they must never reach an external receiver. The list is
// an allow-list on purpose: a new event family is private until someone
// decides it is safe to publish.
package eventpolicy

import "strings"

// TestEventType is the synthetic delivery the webhook "send test" action
// queues. It is never subscribable, but the delivery worker may send it.
const TestEventType = "dms.webhook.test.v1"

// prefixes are the externally subscribable event families. Each entry ends
// with the domain's trailing dot so "dms.documents.x" cannot ride on
// "dms.document.".
var prefixes = []string{
	"dms.document.",
	"dms.version.",
	"dms.folder.",
	"dms.workflow.",
	"dms.signature.",
	"dms.task.",
	"dms.tag.",
	"dms.comment.",
	"dms.record.",
}

// Subscribable reports whether an event subject (or CloudEvent type, which
// uses the same dms.{domain}.{action}.v1 taxonomy) may be delivered to a
// tenant-configured external receiver.
func Subscribable(subject string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(subject, p) && len(subject) > len(p) {
			return true
		}
	}
	return false
}

// Deliverable reports whether the webhook delivery worker may send a queued
// delivery of this event type: a subscribable event, or the synthetic test
// event. Rows queued before the allow-list existed (e.g. a password-reset
// event) fail this check and are dead-lettered unsent.
func Deliverable(eventType string) bool {
	return eventType == TestEventType || Subscribable(eventType)
}

// Families returns the JetStream subscription wildcards ("dms.document.>")
// for the subscribable families, so consumers subscribe only to what they
// are allowed to forward.
func Families() []string {
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		out = append(out, p+">")
	}
	return out
}
