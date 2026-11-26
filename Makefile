SHELL := /bin/bash
.DEFAULT_GOAL := help

MERMAID_CLI  := npx --yes @mermaid-js/mermaid-cli@11
MARKDOWNLINT := npx --yes markdownlint-cli2@0.18.1
DIAGRAM_DIR  := docs/diagrams
DIAGRAMS_SRC := $(wildcard $(DIAGRAM_DIR)/*.mmd)
DIAGRAMS_PNG := $(DIAGRAMS_SRC:.mmd=.png)

.PHONY: help
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-30s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

COMPOSE := docker compose --env-file .env -f deploy/compose/compose.yaml

.env:
	cp .env.example .env

.PHONY: up
up: .env ## Build and start the local stack
	$(COMPOSE) up -d --build

.PHONY: down
down: ## Stop the stack (data volumes are kept)
	$(COMPOSE) down

.PHONY: clean
clean: ## Stop the stack and delete its data volumes
	$(COMPOSE) down -v

.PHONY: ps
ps: ## Show container status
	$(COMPOSE) ps

.PHONY: logs
logs: ## Follow service logs (make logs s=router)
	$(COMPOSE) logs -f $(s)

.PHONY: load
load: ## Generate log files into the ingest inbox (make load args="-target-gb 0.1")
	$(COMPOSE) run --rm loadgen $(args)

.PHONY: demo
demo: ## Lifecycle demo on an empty stack: make clean up demo
	scripts/demo.sh

.PHONY: test-failure-dlm-crash
test-failure-dlm-crash: ## Failure test: kill dlm while it copies a partition to COLD
	python3 test/failure/run.py dlm-crash

.PHONY: test-failure-lost-cold-file
test-failure-lost-cold-file: ## Failure test: lose a COLD file before verification
	python3 test/failure/run.py lost-cold-file

.PHONY: test
test: ## Run Go vet and unit tests
	go vet ./...
	go test ./...

.PHONY: docs-diagrams
docs-diagrams: $(DIAGRAMS_PNG) ## Render Mermaid diagrams to PNG

$(DIAGRAM_DIR)/%.png: $(DIAGRAM_DIR)/%.mmd
	$(MERMAID_CLI) --quiet --input "$<" --output "$@" --scale 2 --backgroundColor white

.PHONY: docs-lint
docs-lint: ## Lint all Markdown files
	$(MARKDOWNLINT) "**/*.md" "#node_modules"

.PHONY: docs
docs: docs-diagrams docs-lint ## Render diagrams and lint documentation

EDA_VENV      := eda/.venv
EDA_PYTHON    := $(EDA_VENV)/bin/python
EDA_NOTEBOOKS := eda/01_nslkdd_eda.ipynb eda/02_cloudtrail_eda.ipynb

.PHONY: eda-setup
eda-setup: ## Create the EDA virtual environment
	python3 -m venv $(EDA_VENV)
	$(EDA_PYTHON) -m pip install --quiet --requirement eda/requirements.txt

.PHONY: eda-data
eda-data: ## Download the raw datasets into data/raw
	eda/download_data.sh

.PHONY: eda
eda: ## Run the EDA notebooks top to bottom and save their outputs
	$(EDA_PYTHON) -m jupyter nbconvert --to notebook --execute --inplace \
		--ExecutePreprocessor.timeout=900 $(EDA_NOTEBOOKS)
	@echo "Updated notebooks, docs/eda/figures, and docs/eda/results"
