PKG    = github.com/sapcc/limes
BINS   = collect migrate serve
PREFIX := /usr

all: $(addprefix build/limes-,$(BINS))

GO            := GOPATH=$(CURDIR)/.gopath GOBIN=$(CURDIR)/build go
GO_BUILDFLAGS :=
GO_LDFLAGS    := -s -w

# These target use the incremental rebuild capabilities of the Go compiler to speed things up.
# If no source files have changed, `go install` exits quickly without doing anything.
build/limes-%: FORCE
	$(GO) install $(GO_BUILDFLAGS) -ldflags '$(GO_LDFLAGS)' '$(PKG)/cmd/limes-$*'

GO_ALLPKGS := $(shell go list $(PKG)/cmd/... $(PKG)/pkg/...)
GO_TESTPKGS := $(shell go list -f '{{if .TestGoFiles}}{{.ImportPath}}{{end}}' $(PKG)/pkg/...)
GO_COVERFILES := $(patsubst %,build/%.cover.out,$(subst /,_,$(GO_TESTPKGS)))

# down below, I need to substitute spaces with commas; because of the syntax,
# I have to get these separators from variables
space := $(null) $(null)
comma := ,

check: all static-check build/cover.html FORCE
prepare-check: FORCE $(patsubst pkg/db/%,pkg/test/%, $(wildcard pkg/db/migrations/*.sql))
	@# Precompile a module used by the unit tests which takes a long time to compile because of cgo.
	$(GO) install github.com/sapcc/limes/vendor/github.com/mattn/go-sqlite3
pkg/test/migrations/%.sql: pkg/db/migrations/%.sql
	@# convert Postgres syntax into SQLite syntax where necessary
	sed 's/BIGSERIAL NOT NULL PRIMARY KEY/INTEGER PRIMARY KEY/' < $< > $@
static-check: FORCE
	gofmt -l cmd pkg
	find cmd pkg -type d -exec golint {} \;
	$(GO) vet $(GO_ALLPKGS)
build/%.cover.out: prepare-check FORCE
	$(GO) test -coverprofile=$@ -covermode=count -coverpkg=$(subst $(space),$(comma),$(GO_ALLPKGS)) $(subst _,/,$*)
build/cover.out: $(GO_COVERFILES)
	pkg/test/util/gocovcat.go $(GO_COVERFILES) > $@
build/cover.html: build/cover.out
	$(GO) tool cover -html $< -o $@

install: FORCE all
	install -d -m 0755    "$(DESTDIR)$(PREFIX)/bin"
	install -D -m 0755 -t "$(DESTDIR)$(PREFIX)/bin" $(addprefix build/limes-,$(BINS))
	install -d -m 0755    "$(DESTDIR)$(PREFIX)/share/limes/migrations"
	install -D -m 0644 -t "$(DESTDIR)$(PREFIX)/share/limes/migrations" $(CURDIR)/pkg/db/migrations/*.sql

clean: FORCE
	rm -f -- $(addprefix build/limes-,$(BINS))

build/docker.tar: clean
	make GO_LDFLAGS="-s -w -linkmode external -extldflags -static" DESTDIR='$(CURDIR)/build/install' install
	( cd build/install && tar cf - . ) > build/docker.tar

DOCKER       := docker
DOCKER_IMAGE := sapcc/limes
DOCKER_TAG   := latest

docker: build/docker.tar
	$(DOCKER) build -t "$(DOCKER_IMAGE):$(DOCKER_TAG)" .

.PHONY: FORCE
