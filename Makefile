# rTail — local development.
#
# Everything runs inside the pinned toolchain container defined in
# tools/Dockerfile, so no Node.js or npm is required on the host.
#
#   make dev        run the app in the foreground   <- start here
#   make up         run it in the background
#   make down       stop the background server
#   make logs       tail the background server
#   make url        print this worktree's URL
#   make build      build the webapp into app/
#   make dist       build the minified webapp into dist/
#   make test       run the test suite
#   make typecheck  type-check the webapp
#   make shell      open a shell in the toolchain container
#   make clean      remove generated assets and dependencies
#
# ** Ports are derived from the worktree path. **
#
# Several worktrees of this repo are often checked out at once, and each one
# needs three things to itself: an HTTP port, a UDP port, and a container name.
# Sharing any of them fails in a confusing way — two servers on one UDP port
# both bind successfully and the loser just never receives a log line.
#
# The offset below is a stable hash of the absolute path, so every worktree
# gets its own fixed set that does not change between runs. Override with
# `make dev PORT=9000` if you want a specific one.

IMAGE := rtail-tools

# 0-499, stable per worktree.
OFFSET   := $(shell printf '%s' "$(CURDIR)" | cksum | awk '{print $$1 % 500}')
PORT     ?= $(shell expr 8000 + $(OFFSET))
UDP_PORT ?= $(shell expr 9000 + $(OFFSET))

SLUG      := $(shell basename "$(CURDIR)" | tr -cd 'a-zA-Z0-9_.-')
CONTAINER := rtail-dev-$(SLUG)
URL       := http://localhost:$(PORT)/

DOCKER_RUN := docker run --rm \
	-u "$$(id -u):$$(id -g)" \
	-e HOME=/tmp \
	-e npm_config_cache=/tmp/.npm \
	-v "$$(pwd):/work" \
	-w /work

DEV_ENV := -e WEB_PORT=$(PORT) -e UDP_PORT=$(UDP_PORT) -p $(PORT):$(PORT)

.PHONY: dev up down logs url build dist test typecheck shell clean image deps

## Run the app in the foreground: builds and watches assets, serves the webapp,
## and feeds it three live demo streams. Ctrl-C to stop.
dev: image deps
	@echo "==> $(URL)"
	$(DOCKER_RUN) -it $(DEV_ENV) $(IMAGE) npm run dev

## Same, detached. Survives between commands; stop it with `make down`.
up: image deps down
	@docker run -d --name $(CONTAINER) \
		-u "$$(id -u):$$(id -g)" \
		-e HOME=/tmp -e npm_config_cache=/tmp/.npm \
		$(DEV_ENV) \
		-v "$$(pwd):/work" -w /work $(IMAGE) npm run dev >/dev/null
	@printf '==> waiting for the server '
	@until curl -sf -o /dev/null --max-time 2 $(URL); do printf '.'; sleep 1; done; echo
	@echo "==> $(URL)"

down:
	@docker rm -f $(CONTAINER) >/dev/null 2>&1 || true

logs:
	@docker logs -f $(CONTAINER)

url:
	@echo "$(URL)"

build: image deps
	$(DOCKER_RUN) $(IMAGE) npm run build

dist: image deps
	$(DOCKER_RUN) $(IMAGE) npm run dist

test: image deps
	$(DOCKER_RUN) $(IMAGE) npm test

typecheck: image deps
	$(DOCKER_RUN) $(IMAGE) npm run typecheck

shell: image deps
	$(DOCKER_RUN) -it $(IMAGE) bash

## Build the toolchain image if it does not exist yet.
image:
	@docker image inspect $(IMAGE) >/dev/null 2>&1 || { \
		echo "==> building $(IMAGE)"; \
		docker build -t $(IMAGE) tools/; \
	}

## Install dependencies if they are missing.
deps:
	@[ -d node_modules ] || { \
		echo "==> installing dependencies"; \
		$(DOCKER_RUN) $(IMAGE) npm install --no-audit --no-fund; \
	}

clean: down
	rm -rf node_modules dist app/css app/bundle.js app/bundle.js.map
