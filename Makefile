# Every target runs in containers; only Docker is needed on the host.
GO_IMAGE := golang:1.27.4-bookworm
NODE_IMAGE := node:22.23.5-bookworm-slim
# Module and build caches live in the git-ignored .cache/ so runs stay fast
# and the container can write them as the calling user.
DOCKER_GO := docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -e GOPATH=/src/.cache/gopath \
  -e GOCACHE=/src/.cache/gocache -e GOFLAGS=-buildvcs=false -v "$$PWD":/src -w /src $(GO_IMAGE)
IMAGE := gpb-api:local
# The conformance suite and load test live in the reference implementation,
# released by tag. One copy, pinned; fetched into the git-ignored .cache/.
CONFORMANCE_REPO := https://github.com/the-vishal-kumar/nodejs-typescript-rest-boilerplate.git
CONFORMANCE_TAG := conformance-v2
SUITE := .cache/loan-api-conformance
export UID := $(shell id -u)
export GID := $(shell id -g)

.PHONY: help install lint vet typecheck build test suite conformance up down image scan bench

help: ## List targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

install: ## Download Go modules
	mkdir -p .cache
	$(DOCKER_GO) go mod download

lint: install ## gofmt check and go vet
	$(DOCKER_GO) sh -c 'test -z "$$(gofmt -l cmd internal migrations)" || { gofmt -l cmd internal migrations; exit 1; }'
	$(DOCKER_GO) go vet ./...

vet: install ## go vet (the compiler is the type checker)
	$(DOCKER_GO) go vet ./...

typecheck: vet ## Alias of vet

build: install ## Compile the API binary to .cache/api
	$(DOCKER_GO) sh -c 'CGO_ENABLED=0 go build -trimpath -o .cache/api ./cmd/api'

test: ## Unit and integration tests (race detector on) against a throwaway PostgreSQL
	mkdir -p .cache
	docker compose -f compose.test.yaml run --rm tests; status=$$?; \
	docker compose -f compose.test.yaml down -v --remove-orphans; exit $$status

suite: ## Fetch the conformance suite at $(CONFORMANCE_TAG) into .cache/
	rm -rf .cache/loan-api-conformance
	mkdir -p .cache
	docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -v "$$PWD/.cache":/out alpine/git:v2.54.0 \
	  clone --quiet --depth 1 --branch $(CONFORMANCE_TAG) $(CONFORMANCE_REPO) /out/loan-api-conformance

# The suite needs admin sign-up on, and TRUST_PROXY=1 so each simulated client
# (its own X-Forwarded-For) gets its own rate-limit bucket.
conformance: suite ## Start a fresh stack and run the shared conformance suite against it
	sed -e 's/^TRUST_PROXY=.*/TRUST_PROXY=1/' -e 's/^ADMIN_SIGNUP_ENABLED=.*/ADMIN_SIGNUP_ENABLED=true/' \
	  .env.sample > .cache/conformance.env
	ENV_FILE=.cache/conformance.env docker compose up --build -d --wait --wait-timeout 180; status=$$?; \
	if [ $$status -eq 0 ]; then \
	  docker run --rm --network host -v "$$PWD/$(SUITE)":/suite:ro -w /suite -e BASE_URL=http://localhost:3000 \
	    $(NODE_IMAGE) node --test 'conformance/*.test.mjs'; status=$$?; \
	fi; \
	ENV_FILE=.cache/conformance.env docker compose down -v --remove-orphans; exit $$status

up: ## Start API and PostgreSQL on http://localhost:3000 (docs at /docs)
	test -f .env || cp .env.sample .env
	docker compose up --build -d --wait

down: ## Stop the local stack
	docker compose down --remove-orphans

image: ## Build the production image
	docker build -t $(IMAGE) .

scan: image ## Scan the image for fixable HIGH/CRITICAL CVEs with Trivy
	docker run --rm -v /var/run/docker.sock:/var/run/docker.sock aquasec/trivy:0.70.0 \
	  image --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 $(IMAGE)

# Same CPU and memory limits for every implementation: compose.bench.yaml.
bench: suite ## Measure this stack with the shared k6 load (results in .cache/bench)
	cp .env.sample .cache/bench.env
	ENV_FILE=.cache/bench.env NAME=go-postgresql LOC_DIRS="cmd internal migrations" GO_MAIN=./cmd/api \
	  bash $(SUITE)/conformance/load/bench.sh
