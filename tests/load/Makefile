# VaultDMS Load Test Runner
# Usage: make <target> [BASE_URL=http://...] [TENANTS=10]

BASE_URL ?= http://localhost:8080/api/v1
WS_URL ?= ws://localhost:8083/ws
TENANTS ?= 10
K6 ?= k6
K6_OUT ?= --out json=results/$(shell date +%Y%m%d-%H%M%S)-$@.json

.PHONY: all doc-crud search upload ocr websocket mixed isolation chaos readiness clean

all: doc-crud search upload mixed isolation
	@echo "All standard load tests complete."

doc-crud:
	$(K6) run $(K6_OUT) -e BASE_URL=$(BASE_URL) -e TENANTS=$(TENANTS) scenarios/01-document-crud.js

search:
	$(K6) run $(K6_OUT) -e BASE_URL=$(BASE_URL) -e TENANTS=$(TENANTS) scenarios/02-search.js

upload:
	$(K6) run $(K6_OUT) -e BASE_URL=$(BASE_URL) -e TENANTS=$(TENANTS) scenarios/03-upload.js

ocr:
	$(K6) run $(K6_OUT) -e BASE_URL=$(BASE_URL) -e TENANTS=$(TENANTS) scenarios/04-ocr-pipeline.js

websocket:
	$(K6) run $(K6_OUT) -e WS_URL=$(WS_URL) -e TENANTS=$(TENANTS) scenarios/05-websocket.js

mixed:
	$(K6) run $(K6_OUT) -e BASE_URL=$(BASE_URL) -e TENANTS=$(TENANTS) scenarios/06-mixed-realistic.js

isolation:
	$(K6) run $(K6_OUT) -e BASE_URL=$(BASE_URL) scenarios/07-cross-tenant-isolation.js

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
	rm -rf results/*.json chaos/*.log
