PKG    = github.com/sapcc/limes
PREFIX := /usr

all: build/limes

# NOTE: This repo uses Go modules, and uses a synthetic GOPATH at
# $(CURDIR)/.gopath that is only used for the build cache. $GOPATH/src/ is
# empty.
GO            := GOPATH=$(CURDIR)/.gopath GOBIN=$(CURDIR)/build go
GO_BUILDFLAGS :=
GO_LDFLAGS    := -s -w

build/limes: FORCE
	$(GO) install $(GO_BUILDFLAGS) -ldflags '$(GO_LDFLAGS)' '$(PKG)/cmd/limes'

# which packages to test with static checkers?
GO_ALLPKGS := $(PKG) $(shell go list $(GO_BUILDFLAGS) $(PKG)/pkg/... $(PKG)/cmd/...)
# which packages to test with `go test`?
GO_TESTPKGS := $(shell go list $(GO_BUILDFLAGS) -f '{{if .TestGoFiles}}{{.ImportPath}}{{end}}' $(PKG) $(PKG)/pkg/... $(PKG)/cmd/...)
# which packages to measure coverage for?
GO_COVERPKGS := $(shell go list $(GO_BUILDFLAGS) $(PKG) $(PKG)/pkg/... | grep -v plugins)
# output files from `go test`
GO_COVERFILES := $(patsubst %,build/%.cover.out,$(subst /,_,$(GO_TESTPKGS)))

# down below, I need to substitute spaces with commas; because of the syntax,
# I have to get these separators from variables
space := $(null) $(null)
comma := ,

check: all static-check build/cover.html FORCE
	@printf "\e[1;32m>> All tests successful.\e[0m\n"
static-check: FORCE
	@if ! hash golint 2>/dev/null; then printf "\e[1;36m>> Installing golint...\e[0m\n"; go get -u golang.org/x/lint/golint; fi
	@printf "\e[1;36m>> gofmt\e[0m\n"
	@if s="$$(gofmt -s -l *.go cmd pkg 2>/dev/null)"                            && test -n "$$s"; then printf ' => %s\n%s\n' gofmt  "$$s"; false; fi
	@printf "\e[1;36m>> golint\e[0m\n"
	@if s="$$(golint . && find cmd pkg -type d -exec golint {} \; 2>/dev/null)" && test -n "$$s"; then printf ' => %s\n%s\n' golint "$$s"; false; fi
	@printf "\e[1;36m>> go vet\e[0m\n"
	@$(GO) vet $(GO_BUILDFLAGS) $(GO_ALLPKGS)

# detailed unit test run (incl. test coverage)
build/%.cover.out: FORCE
	@printf "\e[1;36m>> go test $(subst _,/,$*)\e[0m\n"
	$(GO) test $(GO_BUILDFLAGS) -ldflags '$(GO_LDFLAGS)' -coverprofile=$@ -covermode=count -coverpkg=$(subst $(space),$(comma),$(GO_COVERPKGS)) $(subst _,/,$*)
build/cover.out: $(GO_COVERFILES)
	$(GO) run $(GO_BUILDFLAGS) pkg/test/util/gocovcat.go $(GO_COVERFILES) > $@
build/cover.html: build/cover.out
	$(GO) tool cover -html $< -o $@

# quick unit test run
quick-check: all static-check $(addprefix quick-check-,$(subst /,_,$(GO_TESTPKGS))) FORCE
quick-check-%:
	@printf "\e[1;36m>> go test $(subst _,/,$*)\e[0m\n"
	$(GO) test $(GO_BUILDFLAGS) -ldflags '$(GO_LDFLAGS)' $(subst _,/,$*)

install: FORCE all
	install -D -m 0755 build/limes "$(DESTDIR)$(PREFIX)/bin/limes"

clean: FORCE
	rm -f -- build/limes

vendor: FORCE
	$(GO) mod tidy
	$(GO) mod vendor

.PHONY: FORCE
