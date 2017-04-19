PKG    = github.com/sapcc/limes
PREFIX := /usr

all: build/limes

GO            := GOPATH=$(CURDIR)/.gopath GOBIN=$(CURDIR)/build go
GO_BUILDFLAGS :=
GO_LDFLAGS    := -s -w

# This target uses the incremental rebuild capabilities of the Go compiler to speed things up.
# If no source files have changed, `go install` exits quickly without doing anything.
build/limes: FORCE
	$(GO) install $(GO_BUILDFLAGS) -ldflags '$(GO_LDFLAGS)' '$(PKG)'

# which packages to test with static checkers?
GO_ALLPKGS := $(PKG) $(shell go list $(PKG)/pkg/...)
# which packages to test with `go test`?
GO_TESTPKGS := $(shell go list -f '{{if .TestGoFiles}}{{.ImportPath}}{{end}}' $(PKG)/pkg/...)
# which packages to measure coverage for?
GO_COVERPKGS := $(shell go list $(PKG)/pkg/... | grep -v plugins)
# output files from `go test`
GO_COVERFILES := $(patsubst %,build/%.cover.out,$(subst /,_,$(GO_TESTPKGS)))

# down below, I need to substitute spaces with commas; because of the syntax,
# I have to get these separators from variables
space := $(null) $(null)
comma := ,

check: all static-check build/cover.html FORCE
	@echo -e "\e[1;32m>> All tests successful.\e[0m"
prepare-check: FORCE $(patsubst pkg/db/%,pkg/test/%, $(wildcard pkg/db/migrations/*.sql))
	@# Precompile a module used by the unit tests which takes a long time to compile because of cgo.
	$(GO) install github.com/sapcc/limes/vendor/github.com/mattn/go-sqlite3
pkg/test/migrations/%.sql: pkg/db/migrations/%.sql
	@# convert Postgres syntax into SQLite syntax where necessary
	sed 's/BIGSERIAL NOT NULL PRIMARY KEY/INTEGER PRIMARY KEY/' < $< > $@
static-check: FORCE
	@if s="$$(gofmt -s -l *.go pkg 2>/dev/null)"                            && test -n "$$s"; then printf ' => %s\n%s\n' gofmt  "$$s"; false; fi
	@if s="$$(golint . && find pkg -type d -exec golint {} \; 2>/dev/null)" && test -n "$$s"; then printf ' => %s\n%s\n' golint "$$s"; false; fi
	$(GO) vet $(GO_ALLPKGS)
build/%.cover.out: prepare-check FORCE
	$(GO) test $(GO_BUILDFLAGS) -ldflags '$(GO_LDFLAGS)' -coverprofile=$@ -covermode=count -coverpkg=$(subst $(space),$(comma),$(GO_COVERPKGS)) $(subst _,/,$*)
build/cover.out: $(GO_COVERFILES)
	pkg/test/util/gocovcat.go $(GO_COVERFILES) > $@
build/cover.html: build/cover.out
	$(GO) tool cover -html $< -o $@

install: FORCE all
	install -D -m 0755 build/limes "$(DESTDIR)$(PREFIX)/bin/limes"
	install -d -m 0755    "$(DESTDIR)$(PREFIX)/share/limes/migrations"
	install -D -m 0644 -t "$(DESTDIR)$(PREFIX)/share/limes/migrations" $(CURDIR)/pkg/db/migrations/*.sql

clean: FORCE
	rm -f -- build/limes

build/docker.tar: clean
	make GO_LDFLAGS="-s -w -linkmode external -extldflags -static" DESTDIR='$(CURDIR)/build/install' install
	( cd build/install && tar cf - . ) > build/docker.tar

DOCKER       := docker
DOCKER_IMAGE := sapcc/limes
DOCKER_TAG   := latest

docker: build/docker.tar
	$(DOCKER) build -t "$(DOCKER_IMAGE):$(DOCKER_TAG)" .

vendor: FORCE
	@# vendoring by https://github.com/holocm/golangvend
	golangvend

.PHONY: FORCE
