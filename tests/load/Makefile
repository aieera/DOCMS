# SeDoc Load Test Runner
# Usage: make <target> [BASE_URL=http://...] [TENANTS=10] [TARGET_VUS=10000]
#
# §16 sign-off run:
#   make run-protocol BASE_URL=https://loadtest.internal/api/v1 \
#                     TENANTS=100 TARGET_VUS=100000 TARGET_RPS=10000
#
# Smoke run on the dev compose stack:
#   make smoke BASE_URL=http://localhost:8080/api/v1 TENANTS=2 TARGET_VUS=200

BASE_URL    ?= http://localhost:8080/api/v1
WS_URL      ?= ws://localhost:8083/ws
TENANTS     ?= 10
TARGET_VUS  ?= 10000
TARGET_RPS  ?= 1000
HOLD        ?= 1h
K6          ?= k6
STAMP       := $(shell date +%Y%m%d-%H%M%S)
K6_OUT_DIR  ?= results
K6_OUT       = --out json=$(K6_OUT_DIR)/$(STAMP)-$@.json

ENV = -e BASE_URL=$(BASE_URL) \
      -e WS_URL=$(WS_URL) \
      -e TENANTS=$(TENANTS) \
      -e TARGET_VUS=$(TARGET_VUS) \
      -e TARGET_RPS=$(TARGET_RPS) \
      -e HOLD_DURATION=$(HOLD)

.PHONY: all doc-crud search upload ocr websocket mixed mixed-v1 isolation \
        browse upload-journey rag signing run-protocol smoke chaos \
        readiness clean prep-results

prep-results:
	@mkdir -p $(K6_OUT_DIR)

# --- legacy scenarios (kept for parity with the earlier harness) ---
all: doc-crud search upload mixed-v1 isolation
	@echo "All standard load tests complete."

doc-crud: prep-results
	$(K6) run $(K6_OUT) $(ENV) scenarios/01-document-crud.js

search: prep-results
	$(K6) run $(K6_OUT) $(ENV) scenarios/02-search.js

upload: prep-results
	$(K6) run $(K6_OUT) $(ENV) scenarios/03-upload.js

ocr: prep-results
	$(K6) run $(K6_OUT) $(ENV) scenarios/04-ocr-pipeline.js

websocket: prep-results
	$(K6) run $(K6_OUT) $(ENV) scenarios/05-websocket.js

mixed-v1: prep-results
	$(K6) run $(K6_OUT) $(ENV) scenarios/06-mixed-realistic.js

isolation: prep-results
	$(K6) run $(K6_OUT) -e BASE_URL=$(BASE_URL) scenarios/07-cross-tenant-isolation.js

# --- §16 scenarios (ADR 0105) -------------------------------------
browse: prep-results
	$(K6) run $(K6_OUT) $(ENV) scenarios/10-browse-search-open.js

upload-journey: prep-results
	$(K6) run $(K6_OUT) $(ENV) scenarios/11-upload-workflow-approve.js

rag: prep-results
	$(K6) run $(K6_OUT) $(ENV) scenarios/12-rag-query.js

signing: prep-results
	$(K6) run $(K6_OUT) $(ENV) scenarios/13-concurrent-signing.js

mixed: prep-results
	$(K6) run $(K6_OUT) $(ENV) scenarios/14-mixed-realistic.js

# Full §16 sign-off protocol — runs the canonical mixed-realistic
# scenario with the full ramp/hold/burst/cool shape, kicks off the
# chaos overlay ~50 min in (during the final 30 min of the 1h hold)
# and bundles everything into a dated results dir.
run-protocol: prep-results
	@RUN_DIR=$(K6_OUT_DIR)/protocol-$(STAMP); \
	 mkdir -p $$RUN_DIR; \
	 echo "Writing artifacts to $$RUN_DIR"; \
	 echo "Starting chaos overlay (background, fires after 50 min)…"; \
	 (sleep 3000 && bash chaos/chaos-tests.sh 1800 > $$RUN_DIR/chaos.log 2>&1) & \
	 $(K6) run --out json=$$RUN_DIR/mixed-protocol.json $(ENV) scenarios/14-mixed-realistic.js \
	   | tee $$RUN_DIR/mixed-protocol.txt; \
	 echo "Run complete — $$RUN_DIR"

smoke: prep-results
	$(K6) run $(K6_OUT) $(ENV) -e HOLD_DURATION=5m scenarios/14-mixed-realistic.js

chaos:
	chmod +x chaos/chaos-tests.sh && bash chaos/chaos-tests.sh

readiness: all isolation
	@echo ""
	@echo "=== Production Readiness Checklist ==="
	@echo "[ ] All SLIs green (check k6 output above)"
	@echo "[ ] Cross-tenant isolation: ZERO violations"
	@echo "[ ] Security scans clean (make -C ../.. security)"
	@echo "[ ] Backups tested (pg_dump restore verified)"
	@echo "[ ] Monitoring dashboards active (Grafana)"
	@echo "[ ] Alerting rules deployed (Prometheus)"
	@echo "[ ] Runbooks written for P1/P2 alerts"
	@echo "[ ] On-call rotation defined in PagerDuty"
	@echo "[ ] Secrets rotated (dms-admin rotate-secrets)"
	@echo "[ ] DNS + TLS configured"
	@echo "[ ] Rate limiting verified"
	@echo "[ ] DLP rules tested"
	@echo "[ ] Helm chart dry-run passed"
	@echo ""

clean:
	rm -rf $(K6_OUT_DIR)/*.json $(K6_OUT_DIR)/*.txt $(K6_OUT_DIR)/protocol-* chaos/*.log
