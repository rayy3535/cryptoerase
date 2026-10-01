GO          ?= go
BIN         := bin/cryptoerase
LDFLAGS     := -s -w
GOLANGCI    := github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.8.0
ACTIONLINT  := github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
COVER_MIN   ?= 90
FUZZTIME    ?= 30s

# package:FuzzTarget pairs, one fuzz run each.
FUZZ_TARGETS := \
	.:FuzzCompareVersions .:FuzzParseFirmwarePolicy .:FuzzSampleOffsets \
	./ata:FuzzParseIstdout ./ata:FuzzParseIdentify ./ata:FuzzParseSanitizeStatus \
	./nvme:FuzzParsers ./nvme:FuzzFormatSpecCDW10 \
	./tcg:FuzzParseLevel0 ./perc:FuzzParse

.PHONY: all build test vet fmt-check tidy-check golangci vulncheck actionlint lint cover fuzz integration release examples clean

all: lint test build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o $(BIN) ./cmd/cryptoerase

test:
	$(GO) test -race -count=1 -shuffle=on ./...

vet:
	$(GO) vet ./...
	$(GO) vet -tags integration ./...

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

tidy-check:
	$(GO) mod tidy -diff

# golangci-lint runs staticcheck, errcheck, gosec, revive and more; see
# .golangci.yml. It covers the integration build tag too.
golangci:
	$(GO) run $(GOLANGCI) run ./...

vulncheck:
	$(GO) run $(GOVULNCHECK) ./...

# actionlint also runs shellcheck on workflow scripts when it is installed.
actionlint:
	$(GO) run $(ACTIONLINT)

lint: fmt-check tidy-check vet golangci actionlint

# cover writes coverage.out and fails below COVER_MIN percent.
cover:
	$(GO) test -count=1 -covermode=atomic -coverprofile=coverage.out ./...
	@$(GO) tool cover -func=coverage.out | tail -1
	@total=$$($(GO) tool cover -func=coverage.out | awk '/^total:/ {sub("%","",$$3); print $$3}'); \
	if awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { exit !(t < m) }'; then \
		echo "coverage $$total% is below $(COVER_MIN)%"; exit 1; fi

fuzz:
	@for t in $(FUZZ_TARGETS); do \
		pkg=$${t%%:*}; fn=$${t##*:}; \
		echo "fuzz $$fn ($$pkg) for $(FUZZTIME)"; \
		$(GO) test -run='^$$' -fuzz="^$$fn$$" -fuzztime=$(FUZZTIME) $$pkg || exit 1; \
	done

# integration needs root (loop devices, NVMe admin commands). It writes only
# to a loop device backed by a temporary file.
integration:
	$(GO) test -c -tags integration -o bin/integration.test .
	sudo ./bin/integration.test -test.v -test.count=1

release:
	@mkdir -p dist
	@for arch in amd64 arm64; do \
		echo "building linux/$$arch"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags='$(LDFLAGS)' \
			-o dist/cryptoerase-linux-$$arch ./cmd/cryptoerase || exit 1; \
	done
	cd dist && sha256sum cryptoerase-linux-* > SHA256SUMS

examples:
	CRYPTOERASE_WRITE_EXAMPLES=1 $(GO) test -count=1 -run TestExamples .

clean:
	rm -rf bin dist coverage.out
