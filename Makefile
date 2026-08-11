# rTail — local development.
#
# Everything runs inside the pinned toolchain container defined in
# tools/Dockerfile, so no Node.js or npm is required on the host.
#
#   make dev     run the app locally with live demo streams  <- start here
#   make build   build the generated assets once
#   make css     recompile stylesheets only
#   make shell   open a shell in the toolchain container
#   make clean   remove generated assets and dependencies

IMAGE := rtail-tools
PORT  ?= 8888

DOCKER_RUN := docker run --rm \
	-u "$$(id -u):$$(id -g)" \
	-e HOME=/tmp \
	-e npm_config_cache=/tmp/.npm \
	-v "$$(pwd):/work" \
	-w /work

.PHONY: dev build css shell clean image deps

## Run the app: builds assets, serves the webapp, and feeds it three live
## demo streams. Open http://localhost:$(PORT)/app — Ctrl-C to stop.
dev: image deps
	@echo "==> http://localhost:$(PORT)/app"
	$(DOCKER_RUN) -it -p $(PORT):$(PORT) -e WEB_PORT=$(PORT) $(IMAGE) sh tools/dev.sh

## Build every generated asset once (css, app.js, vendor bundle).
build: image deps
	$(DOCKER_RUN) $(IMAGE) sh tools/build-assets.sh

## Recompile stylesheets only — the fast loop when working on the UI.
css: image
	$(DOCKER_RUN) $(IMAGE) sass --no-source-map app/scss/main.scss app/css/main.css

## Shell into the toolchain container.
shell: image
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
		$(DOCKER_RUN) $(IMAGE) npm install --ignore-scripts --no-audit --no-fund; \
	}

clean:
	rm -rf node_modules dist app/css app/vendor app/app.js
