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

install: FORCE all
	install -D -m 0755 -t "$(DESTDIR)$(PREFIX)/bin" $(addprefix build/limes-,$(BINS))
	install -D -m 0644 -t "$(DESTDIR)$(PREFIX)/share/limes/migrations" $(CURDIR)/pkg/db/migrations/*.sql

.PHONY: FORCE
