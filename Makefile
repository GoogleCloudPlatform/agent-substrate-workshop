# Agent Substrate workshop. Run `make help` for targets.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help
MODULE ?=
RESET ?=

help: ## Show targets
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

preflight: ## Check tools and cluster prerequisites
	@scripts/preflight.sh
gcp: ## Create bucket, Artifact Registry repo, IAM bindings
	@scripts/gcp-resources.sh
install: ## Install Agent Substrate (foreground)
	@scripts/install-substrate.sh
install-bg: ## Install Agent Substrate in the background (log: .workshop/install.log)
	@mkdir -p .workshop && nohup scripts/install-substrate.sh >.workshop/install.log 2>&1 & echo "installing… tail -f .workshop/install.log"
install-status: ## Show background install progress
	@tail -n 20 .workshop/install.log && source scripts/lib.sh && { user_has_ate || path_hint; }
dashboards: ## Create/update Cloud Monitoring dashboards
	@scripts/dashboards.sh
build-agent: ## ko build the agent -> .workshop/agent-image
	@scripts/build.sh agent
build-gateway: ## ko build the gateway -> .workshop/gateway-image
	@scripts/build.sh gateway
build: build-agent build-gateway ## Build the agent and gateway images
gateway: ## Build and deploy the gateway
	@scripts/build.sh gateway && scripts/gateway.sh
egress: ## Apply an EgressPolicy: make egress POLICY=file (ACTOR=name | ALL=1)
	@scripts/egress.sh apply "$(POLICY)" "$(if $(ALL),--all,--actor=$(ACTOR))"
tools: ## Build local helper binaries into bin/
	@mkdir -p bin && cd app && go build -o ../bin/egressctl ./cmd/egressctl
load: ## Burst chat traffic across swarm actors
	@scripts/load.sh
observe: ## Deploy the gateway PodMonitoring and the Prometheus UI
	@scripts/observability.sh
prometheus-ui: ## Port-forward the Prometheus UI to http://localhost:9090
	@source scripts/lib.sh && load_env && port_forward prom-ui $$NAMESPACE svc/prometheus-ui 9090 9090 && echo "open http://localhost:9090"
forward-router: ## Port-forward the atenet-router to http://localhost:8080
	@source scripts/lib.sh && load_env && port_forward router ate-system svc/atenet-router 8080 80 && echo "open http://localhost:8080"
forward-gateway: ## Port-forward the gateway to http://localhost:8081
	@source scripts/lib.sh && load_env && port_forward gateway $$NAMESPACE svc/gateway 8081 80 && echo "open http://localhost:8081"
catch-up: ## Bring your environment to the start of MODULE=N (RESET=1 rebuilds that module's template)
	@RESET="$(RESET)" scripts/catch-up.sh "$(MODULE)"
verify: ## Check the end state of MODULE=N
	@scripts/verify/$$(printf '%02d' "$(MODULE)").sh
e2e: ## Run every module via catch-up + verify
	@for n in 1 2 3 4 5 6 7; do scripts/catch-up.sh $$n && scripts/verify/$$(printf '%02d' $$((n-1))).sh || exit 1; done
test: test-go test-scripts ## Run all offline tests
test-go:
	@cd app && go vet ./... && go test ./...
test-scripts:
	@bash scripts/test/lib_test.sh && bash scripts/test/render_test.sh && bash scripts/test/dashboards_test.sh && bash scripts/test/solutions_test.sh
stop-forwards: ## Stop background port-forwards
	@source scripts/lib.sh && stop_port_forwards
teardown: ## Delete workshop resources (keeps the cluster)
	@scripts/teardown.sh
clean: ## Remove local build output (bin/, dist/)
	@rm -rf bin dist
.PHONY: help preflight gcp install install-bg install-status dashboards build-agent build-gateway build gateway egress tools load observe prometheus-ui forward-router forward-gateway catch-up verify e2e test test-go test-scripts stop-forwards teardown clean
