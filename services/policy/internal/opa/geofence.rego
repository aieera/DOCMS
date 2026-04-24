# Wave 15.2 — geofence decision rule.
#
# This module mirrors the Go decider in services/policy/internal/service/
# geofence.go. The Go implementation is authoritative for hot-path
# enforcement (middleware calls it on every request). This rego module
# is the portable form operators can embed in an external OPA cluster or
# feed into a rego-test harness.
#
# Input shape:
#   {
#     "tenant_id": "...",
#     "resource_scope": "tenant|workspace|document",
#     "resource_id": "...",
#     "action": "read|write|admin|*",
#     "source_ip": "1.2.3.4",
#     "source_country": "US",
#     "policies": [
#       { "scope": "...", "scope_id": "...", "mode": "allow|deny|step_up",
#         "country_codes": [...], "cidr_denylist": [...], "cidr_allowlist": [...],
#         "apply_to": "read|write|admin|*", "enabled": true }
#     ]
#   }
#
# Result: data.geofence.decision
#   { "allow": bool, "require_step_up": bool, "reason": string }

package geofence

default decision := {"allow": true, "require_step_up": false, "reason": "no_policy"}

# Deny by CIDR denylist always wins.
decision := {"allow": false, "require_step_up": false, "reason": "cidr_denylist"} {
    some p in input.policies
    p.enabled
    applies_to_action(p.apply_to, input.action)
    some cidr in p.cidr_denylist
    net.cidr_contains(cidr, input.source_ip)
}

# Deny by country.
decision := {"allow": false, "require_step_up": false, "reason": "country_deny"} {
    some p in input.policies
    p.enabled
    p.mode == "deny"
    applies_to_action(p.apply_to, input.action)
    input.source_country != ""
    input.source_country == p.country_codes[_]
}

# Deny when allow-policy has a country list and request isn't in it.
decision := {"allow": false, "require_step_up": false, "reason": "country_not_in_allowlist"} {
    some p in input.policies
    p.enabled
    p.mode == "allow"
    applies_to_action(p.apply_to, input.action)
    count(p.country_codes) > 0
    not country_matches(p.country_codes, input.source_country)
}

# Step-up on country match with mode=step_up.
decision := {"allow": true, "require_step_up": true, "reason": "step_up_required"} {
    some p in input.policies
    p.enabled
    p.mode == "step_up"
    applies_to_action(p.apply_to, input.action)
    input.source_country == p.country_codes[_]
}

applies_to_action(apply_to, _) {
    apply_to == "*"
}
applies_to_action(apply_to, action) {
    apply_to == action
}

country_matches(codes, cc) {
    cc != ""
    cc == codes[_]
}
