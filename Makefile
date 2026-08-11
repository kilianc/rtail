# rTail — local development.
#
# Everything runs inside the pinned toolchain container defined in
# tools/Dockerfile, so no Node.js or npm is required on the host.
#
#   make dev        run the app locally with live demo streams  <- start here
#   make build      build the webapp into app/
#   make dist       build the minified, self-contained webapp into dist/
#   make test       run the test suite
#   make typecheck  type-check the webapp
#   make shell      open a shell in the toolchain container
#   make clean      remove generated assets and dependencies

IMAGE := rtail-tools
PORT  ?= 8888

DOCKER_RUN := docker run --rm \
	-u "$$(id -u):$$(id -g)" \
	-e HOME=/tmp \
	-e npm_config_cache=/tmp/.npm \
	-v "$$(pwd):/work" \
	-w /work

.PHONY: dev build dist test typecheck shell clean image deps

## Run the app: builds and watches assets, serves the webapp, and feeds it
## three live demo streams. Open http://localhost:$(PORT)/app — Ctrl-C to stop.
dev: image deps
	@echo "==> http://localhost:$(PORT)/app"
	$(DOCKER_RUN) -it -p $(PORT):$(PORT) -e WEB_PORT=$(PORT) $(IMAGE) npm run dev

## Build the webapp into app/.
build: image deps
	$(DOCKER_RUN) $(IMAGE) npm run build

## Build the minified, self-contained webapp into dist/.
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

clean:
	rm -rf node_modules dist app/css app/bundle.js app/bundle.js.map
