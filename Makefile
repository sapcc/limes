PKG    = github.com/sapcc/limes
BINS   = collect migrate
PREFIX := /usr

all: $(addprefix build/limes-,$(BINS))

GO            := GOPATH=$(CURDIR)/.gopath GOBIN=$(CURDIR)/build go
GO_BUILDFLAGS :=
GO_LDFLAGS    := -s -w

# These target use the incremental rebuild capabilities of the Go compiler to speed things up.
# If no source files have changed, `go install` exits quickly without doing anything.
build/limes-%: FORCE
	$(GO) install $(GO_BUILDFLAGS) -ldflags '$(GO_LDFLAGS)' '$(PKG)/cmd/limes-$*'

check: prepare-check FORCE
	@$(GO) test $(shell go list $(PKG)/pkg/...)

# Precompile a module used by the unit tests which takes a long time to compile because of cgo.
prepare-check: FORCE
	@$(GO) install github.com/sapcc/limes/vendor/github.com/mattn/go-sqlite3

install: FORCE all
	install -D -m 0755 -t "$(DESTDIR)$(PREFIX)/bin" $(addprefix build/limes-,$(BINS))
	install -D -m 0644 -t "$(DESTDIR)$(PREFIX)/share/limes/migrations" $(CURDIR)/pkg/db/migrations/*.sql

.PHONY: FORCE
