VERSION := `git log -n 1 | grep commit | sed 's/commit //g' | head -n 1`
LDFLAGS := -X main.version=$(VERSION)
GO := "$(GOROOT)/bin/go"
GOBIN := $(GOBIN)
GO111MODULE := on
LINTERVERSION := v1.16.0

all:  build # vars formatting lint test

vars:
	@echo "====== Makefile internal variables:"
	@echo "VERSION: '$(VERSION)'"
	@echo "LDFLAGS: '$(LDFLAGS)'"
	@echo "GO: '$(GO)'"
	@echo "======\n\n"

linux: vars formatting lint test
	GOOS=linux $(GO) build -v -ldflags "$(LDFLAGS)" -tags 'netcgo=1' ./cmd/akubra

formatting:
	$(GO) get golang.org/x/tools/cmd/goimports

lint: vars deps-lint
	$(LINTERVERSION)/golangci-lint run internal/akubra/* internal/brim/* \
	--skip-dirs ./tmp \
	--disable=dupl \
	--disable=gosec \
	--deadline=600s \
	--disable=goimports \
	--disable=structcheck \
	--disable=typecheck \
	--fast

lint-slow: deps-lint
	$(LINTERVERSION)/golangci-lint run internal/akubra/* internal/brim/* \
	--skip-dirs ./tmp \
	--disable=dupl \
	--deadline=600s \
	--disable=typecheck \
	--disable=structcheck \
	--enable=goimports \
	--fast

deps-lint:
	curl -sfL https://install.goreleaser.com/github.com/golangci/golangci-lint.sh | sh -s -- -b $(LINTERVERSION)

build: vars lint
        # Enable netcgo, then name resolution will use systems dns caches
	$(GO) build -v -ldflags "$(LDFLAGS)" -tags 'netcgo=1' ./cmd/akubra

build-bare-linux: var deps lint
	CGO_ENABLED=0 GOOS=linux $(GO) build -v -a -installsuffix cgo -ldflags '-extldflags "-static"' -o akubra ./cmd/akubra

install-junit-report:
	GOBIN=$(GOBIN) go install github.com/jstemmer/go-junit-report

test: deps install-junit-report
	$(GO) test -v -race -cover $$(go list ./... | grep -v /vendor/)  | go-junit-report > tests.xml

clean:
	rm -rf $(LINTERVERSION)
	$(GO) clean .
