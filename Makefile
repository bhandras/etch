.PHONY: help build test fmt fmt-check commitmsg-lint commitlint commitmsg-fmt

GOCC ?= go

TOOLS_DIR := tools
COMMITMSG_DIR := $(TOOLS_DIR)/commitmsg
LLFORMAT_DIR := $(TOOLS_DIR)/llformat
LLFORMAT_PKG := github.com/bhandras/llformat/cmd/llformat
LLFORMAT_BIN := $(CURDIR)/$(LLFORMAT_DIR)/bin/llformat
BINDIR ?= bin
HARNESS_BIN ?= $(BINDIR)/harness

help:
	@printf '%s\n' \
		'Targets:' \
		'  build           Build the harness binary into bin/harness.' \
		'  test            Run the Go test suite.' \
		'  fmt             Format handwritten Go source with llformat.' \
		'  fmt-check       Run fmt and fail if git sees formatting changes.' \
		'  commitmsg-lint  Lint commit messages. Use range=, commit=, or file=.' \
		'  commitlint      Alias for commitmsg-lint.' \
		'  commitmsg-fmt   Format a commit message. Use file= [inplace=1] or commit=.'

build:
	@mkdir -p $(BINDIR)
	$(GOCC) build -trimpath -o $(HARNESS_BIN) ./cmd/harness

test:
	$(GOCC) test ./...

$(LLFORMAT_BIN): $(LLFORMAT_DIR)/go.mod $(LLFORMAT_DIR)/tools.go
	@mkdir -p $(LLFORMAT_DIR)/bin
	cd $(LLFORMAT_DIR); GOBIN="$(CURDIR)/$(LLFORMAT_DIR)/bin" \
		$(GOCC) install -trimpath $(LLFORMAT_PKG)

fmt: $(LLFORMAT_BIN)
	@find . -type f -name '*.go' \
		-not -path './.git/*' \
		-not -path './tools/llformat/bin/*' \
		-not -path './vendor/*' \
		-not -path './third_party/*' \
		-print0 | xargs -0 $(LLFORMAT_BIN) -w

fmt-check: fmt
	@if [ -d .git ]; then \
		if test -n "$$(git status --porcelain)"; then \
			echo 'code not formatted correctly, please run make fmt'; \
			git status --short; \
			git diff; \
			exit 1; \
		fi; \
	else \
		echo 'fmt-check: no .git directory; skipped dirty-worktree check'; \
	fi

commitmsg-lint:
	@if [ -n "$(range)" ]; then \
		cd $(COMMITMSG_DIR); $(GOCC) run . lint --range "$(range)"; \
	elif [ -n "$(commit)" ]; then \
		cd $(COMMITMSG_DIR); $(GOCC) run . lint --commit "$(commit)"; \
	elif [ -n "$(file)" ]; then \
		cd $(COMMITMSG_DIR); $(GOCC) run . lint --file "$(abspath $(file))"; \
	elif [ -d .git ]; then \
		cd $(COMMITMSG_DIR); $(GOCC) run . lint --commit HEAD; \
	else \
		echo "Error: provide range=<rev-range>, commit=<rev>, or file=<path>."; \
		exit 1; \
	fi

commitlint: commitmsg-lint

commitmsg-fmt:
	@if [ -n "$(file)" ]; then \
		if [ "$(inplace)" = "1" ]; then \
			cd $(COMMITMSG_DIR); $(GOCC) run . fmt --file "$(abspath $(file))" --in-place \
				$(if $(filter 1,$(decode)),--decode-escaped-newlines,); \
		else \
			cd $(COMMITMSG_DIR); $(GOCC) run . fmt --file "$(abspath $(file))" \
				$(if $(filter 1,$(decode)),--decode-escaped-newlines,); \
		fi; \
	elif [ -n "$(commit)" ]; then \
		cd $(COMMITMSG_DIR); $(GOCC) run . fmt --commit "$(commit)" \
			$(if $(filter 1,$(decode)),--decode-escaped-newlines,); \
	else \
		echo "Error: provide file=<path> or commit=<rev>."; \
		exit 1; \
	fi
