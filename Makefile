# The one command that runs the whole backend stack: `make up`.
# Everything it needs is generated locally and git-ignored (secrets/, certs/).

COMPOSE ?= docker compose

.PHONY: up down restart build logs ps config secrets cert clean help

up: secrets cert ## Build and start the whole stack, then wait until it is ready
	$(COMPOSE) up -d --build
	@COMPOSE="$(COMPOSE)" sh scripts/wait-ready.sh
	@echo "backend: https://localhost:$${INGRESS_PORT:-8443} (self-signed certificate; see docs/DEPLOY.md)"

down: ## Stop the stack and keep its data
	$(COMPOSE) down

restart: ## Rebuild and recreate the services, keeping data
	$(COMPOSE) up -d --build --force-recreate
	@COMPOSE="$(COMPOSE)" sh scripts/wait-ready.sh

build: ## Build the service images without starting anything
	$(COMPOSE) build

logs: ## Follow every service's log
	$(COMPOSE) logs -f

ps: ## Show service status
	$(COMPOSE) ps

config: secrets cert ## Validate and print the resolved Compose file
	$(COMPOSE) config

secrets: ## Create any missing secret (never overwrites an existing one)
	@sh scripts/make-secrets.sh

cert: ## Create the local TLS certificate if none exists
	@sh scripts/make-cert.sh

clean: ## Stop the stack and DELETE its data volumes (destructive)
	$(COMPOSE) down -v

help: ## List the targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-9s %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
