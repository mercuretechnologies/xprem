DOCKER_FLAG := $(findstring docker, $(MAKECMDGOALS))
HTML_FLAG := $(findstring html, $(MAKECMDGOALS))
MAKEFLAGS += --silent

# Pinned so a new upstream release can't turn CI red without a code change.
DEADCODE_VERSION := v0.30.0
STATICCHECK_VERSION := v0.6.1
SQLC_VERSION := v1.31.1

build:
ifeq ($(DOCKER_FLAG),docker)
	docker-compose build
else
	go build ./...
endif

up:
ifeq ($(DOCKER_FLAG),docker)
	docker-compose up -d
else
	reflex -r '\.go$$' -s -- sh -c "go run cmd/api/main.go"
endif

down:
ifeq ($(DOCKER_FLAG),docker)
	docker-compose down
else
	echo "Not applicable locally. Stop the application manually."
endif

# Regenerates internal/database/postgres/pgdb from the migrations (schema) and
# queries dirs. Run after touching any .sql file. No local install needed.
sqlc:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

# Both tools always run before failing, so one `make lint` reports every finding.
# They are complementary: staticcheck catches unused unexported identifiers (incl. struct
# fields), deadcode catches exported funcs that nothing reachable from main calls.
lint:
	rc=0; \
	echo "==> staticcheck U1000 (unused unexported identifiers)"; \
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) -checks 'U1000' ./... || rc=1; \
	echo "==> deadcode (funcs unreachable from main; -test keeps test-only helpers alive)"; \
	out=$$(go run golang.org/x/tools/cmd/deadcode@$(DEADCODE_VERSION) -test ./...) || rc=1; \
	if [ -n "$$out" ]; then echo "$$out"; rc=1; fi; \
	if [ $$rc -ne 0 ]; then \
		echo "==> dead code found: delete it, or wire it up to a reachable path."; \
	else \
		echo "==> no dead code"; \
	fi; \
	$(MAKE) lint_ee_headers || rc=1; \
	exit $$rc

# Every source file under a directory named ee/ must carry the EE license
# header. It is the only marker that travels with a file copied out of this
# repository, where GitHub advertises the whole repo as MIT. Directories are
# discovered, not listed, so a future apps/*/src/ee/ is covered on day one.
EE_HEADER := Mercure Technologies Enterprise Edition

lint_ee_headers:
	rc=0; n=0; \
	echo "==> EE license headers"; \
	for f in $$(find . -name node_modules -prune -o -path ./.git -prune -o -type f \
		\( -name '*.go' -o -name '*.ts' -o -name '*.tsx' \) -print | grep '/ee/'); do \
		n=$$((n+1)); \
		head -6 "$$f" | grep -q '$(EE_HEADER)' || { echo "    missing header: $$f"; rc=1; }; \
	done; \
	if [ $$rc -ne 0 ]; then \
		echo "==> copy the 3-line header from any file in ee/ into the files above."; \
		exit 1; \
	fi; \
	echo "==> $$n files, all carrying the EE license header"; \
	echo "==> EE files outside an ee/ directory"; \
	rc=0; \
	for f in $$(grep -rl '$(EE_HEADER)' --include='*.go' --include='*.ts' --include='*.tsx' \
		--exclude-dir=node_modules . | grep -v '/ee/'); do \
		echo "    EE header outside ee/: $$f"; rc=1; \
	done; \
	if [ $$rc -ne 0 ]; then \
		echo "==> a file that declares itself Enterprise must live under an ee/ directory."; \
		exit 1; \
	fi; \
	echo "==> none"

test_app:
ifeq ($(DOCKER_FLAG),docker)
	docker-compose --profile test run --rm -e "" ota-server-test sh -c "go test -race ./internal/cache/ && go test -v -coverpkg=./... -coverprofile=coverage.out ./..."
else
	$(MAKE_COVERAGE_CMD)
endif

test_app_watch:
	find . -name '*.go' | entr -n -c $(MAKE) test_app $(DOCKER_FLAG) $(HTML_FLAG)


# -coverpkg=./... credits every package a test actually executes, so the
# integration tests in ./test count toward the server code they traverse
# instead of only their own helpers.
define MAKE_COVERAGE_CMD
	go test -race ./internal/cache/ && \
	go test -v -coverpkg=./... -coverprofile=coverage.out ./... && \
	$(call CLEAN_COVERAGE) && \
	$(call PRINT_TOTAL) && \
	$(call GENERATE_HTML)
endef

# Dropped from the report: test helpers, cmd entrypoints, and the sqlc
# generated queries (pgdb) whose coverage is meaningless.
define CLEAN_COVERAGE
	if [ "$(shell uname -s)" = "Darwin" ]; then \
		sed -i '' -e '/test/d' -e '/cmd/d' -e '/pgdb/d' coverage.out; \
	else \
		sed -i '/test/d;/cmd/d;/pgdb/d;' coverage.out; \
	fi
endef

# The one number to watch: the real cross-package total. The per-package
# "coverage:" lines above it only measure each package against its own tests.
define PRINT_TOTAL
	go tool cover -func=coverage.out | tail -1
endef

define GENERATE_HTML
	if [ "$(HTML_FLAG)" = "html" ]; then \
		go tool cover -html=coverage.out -o coverage.html && \
		echo 'Coverage report generated: coverage.html'; \
	fi
endef

.PHONY: docker html lint lint_ee_headers sqlc
