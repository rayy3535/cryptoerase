GO      ?= go
BIN     := bin/cryptoerase
LDFLAGS := -s -w

.PHONY: all build test vet fmt-check release examples clean

all: fmt-check vet test build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o $(BIN) ./cmd/cryptoerase

test:
	$(GO) test -race -count=1 ./...

vet:
	$(GO) vet ./...

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

release:
	@mkdir -p dist
	@for arch in amd64 arm64; do \
		echo "building linux/$$arch"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags='$(LDFLAGS)' \
			-o dist/cryptoerase-linux-$$arch ./cmd/cryptoerase || exit 1; \
	done
	cd dist && sha256sum cryptoerase-linux-* > SHA256SUMS

examples:
	CRYPTOERASE_WRITE_EXAMPLES=1 $(GO) test -count=1 -run TestWriteExamples .

clean:
	rm -rf bin dist
