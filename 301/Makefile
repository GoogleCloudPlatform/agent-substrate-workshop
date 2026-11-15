# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Agent Substrate 301: Agent Substrate x Gemini Enterprise Agent Platform. Run `make help` for targets.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help
MODULE ?=
RESET ?=
ALL ?=
REV ?= v1
SKILL ?= ledger-pool-triage

help: ## Show targets
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "  Knobs in [brackets] go after the target: make verify MODULE=0"
	@echo "  Everything else is set in workshop.env (WORKSHOP_ENV_FILE=path picks another file)."

preflight: ## Check local tools, uniform-CPU node pools, and GKE 1.37+ prerequisites
	@scripts/preflight.sh
gcp: ## Enable APIs, create bucket/registry/IAM, and start async gateway & runtime bootstrap
	@scripts/gcp-resources.sh
computeclass: ## Render and apply the quota-safe ComputeClass/substrate-gvisor manifest
	@scripts/render.sh modules/00-setup/manifests/computeclass.yaml.tmpl | kubectl apply -f -
install: ## Install Agent Substrate v1.0.0 in the foreground
	@scripts/install-substrate.sh
install-bg: ## Install Agent Substrate v1.0.0 in the background (log: .workshop/install.log)
	@scripts/install-bg.sh
install-status: ## Show background Substrate and cloud runtime bootstrap progress
	@scripts/install-bg.sh --status
patch-router: ## Idempotently configure atenet-router SPIFFE allowlist, park budget, and HTTP bridge
	@scripts/patch-router.sh
prod-sim: ## Build and deploy the live degraded ledger-api Target Service and ops-hub Operations Hub
	@scripts/prod-sim.sh
build-agent: ## Build and push the Incident Actor image via ko (.workshop/agent-image)
	@scripts/build.sh agent --check-changed
build-gateway: ## Build and push the Governed Broker image via ko (.workshop/gateway-image)
	@scripts/build.sh sre-gateway
gateway: ## Render and deploy sre-gateway (Governed Broker + Incident Console)
	@scripts/sre-gateway.sh
publish-skill: ## Package and publish a Procedural Skill revision to Skill Registry [REV=v1|v2]
	@REV="$(REV)" SKILL="$(SKILL)" scripts/publish-skill.sh
governance: ## Provision Model Armor, Auth Manager 3LO, and ingress GCPAuthzPolicy
	@scripts/setup-governance.sh
audit-snapshot: ## Audit Actor specs, workspace, and GCS snapshots for zero bearer tokens
	@scripts/audit-snapshot.sh
observe: ## Apply GMP PodMonitoring/sre-gateway and show Cloud Trace & Monitoring links
	@scripts/observability.sh
eval: ## Run pure-Go 4-rubric online evaluations against sre-gateway [MIN_SCORE=0.80]
	@mkdir -p bin && (cd app && go build -o ../bin/evalrunner ./cmd/evalrunner)
	@source scripts/lib.sh && load_env && \
	  gw_url="$${EVAL_GATEWAY_URL:-http://127.0.0.1:8081}"; \
	  started_pf=0; \
	  if [[ -z "$${EVAL_GATEWAY_URL:-}" ]] && ! _is_sre_gateway_url "$$gw_url"; then \
	    ephemeral_port_forward eval-gw "$$NAMESPACE" svc/sre-gateway 18082 8080; \
	    gw_url="$$PF_URL"; \
	    started_pf=1; \
	  fi; \
	  trap '[[ "$$started_pf" == 1 ]] && stop_port_forward eval-gw || true' EXIT; \
	  ./bin/evalrunner --gateway-url "$$gw_url" --min-score "$${MIN_SCORE:-0.80}" --output "$$STATE_DIR/eval-report.json"
e2e: ## Run end-to-end catch-up and module verification across Modules 00-07 on cluster
	@scripts/catch-up.sh 7 && for m in 0 1 2 3 4 5 6 7; do scripts/verify/0$$m.sh || exit 1; done
tools: ## Build local helper binaries into bin/
	@mkdir -p bin && cd app && go build -o ../bin/ledger-verify ./cmd/ledger-verify && go build -o ../bin/evalrunner ./cmd/evalrunner
forward-gateway: ## Port-forward sre-gateway Incident Console to http://localhost:8081
	@source scripts/lib.sh && load_env && stop_port_forward gateway && port_forward gateway $$NAMESPACE svc/sre-gateway 8081 8080 && open_hint 8081
forward-ops-hub: ## Port-forward ops-hub to http://localhost:8082
	@source scripts/lib.sh && load_env && stop_port_forward ops-hub && port_forward ops-hub $$SIM_NAMESPACE svc/ops-hub 8082 8080 && open_hint 8082
forward-ledger: ## Port-forward ledger-api to http://localhost:8083
	@source scripts/lib.sh && load_env && stop_port_forward ledger-api && port_forward ledger-api $$SIM_NAMESPACE svc/ledger-api 8083 8080 && open_hint 8083
stop-forwards: ## Stop background port-forwards
	@source scripts/lib.sh && stop_port_forwards
catch-up: ## Bring your environment to the start of a module [MODULE=N RESET=1]
	@RESET="$(RESET)" scripts/catch-up.sh "$(MODULE)"
verify: ## Verify the end state of a module [MODULE=N]
	@scripts/verify/$$(printf '%02d' "$(MODULE)").sh
test: test-go test-scripts ## Run all offline Go and shell contract tests
test-go: ## Run go vet and the Go behavioral tests
	@cd app && go vet ./... && go test ./...
test-scripts: ## Run the offline shell and hygiene tests
	@bash scripts/test/lib_test.sh && bash scripts/test/preflight_test.sh && bash scripts/test/render_test.sh && bash scripts/test/solutions_test.sh && bash scripts/test/skill_publish_test.sh && bash scripts/test/governance_test.sh && bash scripts/test/observability_test.sh && bash scripts/test/hygiene_test.sh && bash scripts/test/docs_sync_test.sh
teardown: ## Delete workshop resources while keeping the cluster [ALL=1 YES=1]
	@scripts/teardown.sh $(if $(ALL),--all)
delete-cluster: ## Release PersistentVolumes and delete the GKE cluster [YES=1]
	@scripts/delete-cluster.sh
clean: ## Remove local build output (bin/, dist/)
	@rm -rf bin dist
.PHONY: help preflight gcp computeclass install install-bg install-status patch-router prod-sim build-agent build-gateway gateway publish-skill governance audit-snapshot observe eval e2e tools forward-gateway forward-ops-hub forward-ledger stop-forwards catch-up verify test test-go test-scripts teardown delete-cluster clean
