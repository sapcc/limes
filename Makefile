################################################################################
# This file is AUTOGENERATED with <https://github.com/sapcc/go-makefile-maker> #
# Edit Makefile.maker.yaml instead.                                            #
################################################################################

MAKEFLAGS=--warn-undefined-variables
# /bin/sh is dash on Debian which does not support all features of ash/bash
# to fix that we use /bin/bash only on Debian to not break Alpine
ifneq (,$(wildcard /etc/os-release)) # check file existence
	ifneq ($(shell grep -c debian /etc/os-release),0)
		SHELL := /bin/bash
	endif
endif

default: build-all

GO_BUILDFLAGS = -mod vendor
GO_LDFLAGS =
GO_TESTENV =

build-all: build/limes

build/limes: FORCE
	go build $(GO_BUILDFLAGS) -ldflags '-s -w $(GO_LDFLAGS)' -o build/limes .

DESTDIR =
ifeq ($(shell uname -s),Darwin)
	PREFIX = /usr/local
else
	PREFIX = /usr
endif

install: FORCE build/limes
	install -D -m 0755 build/limes "$(DESTDIR)$(PREFIX)/bin/limes"

# which packages to test with "go test"
GO_TESTPKGS := $(shell go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...)
# which packages to measure coverage for
GO_COVERPKGS := $(shell go list ./... | command grep -Ev '/plugins')
# to get around weird Makefile syntax restrictions, we need variables containing a space and comma
space := $(null) $(null)
comma := ,

check: FORCE build-all static-check build/cover.html
	@printf "\e[1;32m>> All checks successful.\e[0m\n"

prepare-static-check: FORCE
	@if ! hash golangci-lint 2>/dev/null; then printf "\e[1;36m>> Installing golangci-lint (this may take a while)...\e[0m\n"; go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; fi

static-check: FORCE prepare-static-check
	@printf "\e[1;36m>> golangci-lint\e[0m\n"
	@golangci-lint run

build/cover.out: FORCE | build
	@printf "\e[1;36m>> go test\e[0m\n"
	@env $(GO_TESTENV) go test $(GO_BUILDFLAGS) -ldflags '-s -w $(GO_LDFLAGS)' -shuffle=on -p 1 -coverprofile=$@ -covermode=count -coverpkg=$(subst $(space),$(comma),$(GO_COVERPKGS)) $(GO_TESTPKGS)

build/cover.html: build/cover.out
	@printf "\e[1;36m>> go tool cover > build/cover.html\e[0m\n"
	@go tool cover -html $< -o $@

build:
	@mkdir $@

vendor: FORCE
	go mod tidy
	go mod vendor
	go mod verify

vendor-compat: FORCE
	go mod tidy -compat=$(shell awk '$$1 == "go" { print $$2 }' < go.mod)
	go mod vendor
	go mod verify

license-headers: FORCE
	@if ! hash addlicense 2>/dev/null; then printf "\e[1;36m>> Installing addlicense...\e[0m\n"; go install github.com/google/addlicense@latest; fi
	find * \( -name vendor -type d -prune \) -o \( -name \*.go -exec addlicense -c "SAP SE" -- {} + \)

clean: FORCE
	git clean -dxf build

help: FORCE
	@printf "\n"
	@printf "\e[1mUsage:\e[0m\n"
	@printf "  make \e[36m<target>\e[0m\n"
	@printf "\n"
	@printf "\e[1mGeneral\e[0m\n"
	@printf "  \e[36mhelp\e[0m                  Display this help.\n"
	@printf "\n"
	@printf "\e[1mBuild\e[0m\n"
	@printf "  \e[36mbuild-all\e[0m             Build all binaries.\n"
	@printf "  \e[36mbuild/limes\e[0m           Build limes.\n"
	@printf "  \e[36minstall\e[0m               Install all binaries. This option understands the conventional 'DESTDIR' and 'PREFIX' environment variables for choosing install locations.\n"
	@printf "\n"
	@printf "\e[1mTest\e[0m\n"
	@printf "  \e[36mcheck\e[0m                 Run the test suite (unit tests and golangci-lint).\n"
	@printf "  \e[36mprepare-static-check\e[0m  Install golangci-lint. This is used in CI, you should probably install golangci-lint using your package manager.\n"
	@printf "  \e[36mstatic-check\e[0m          Run golangci-lint.\n"
	@printf "  \e[36mbuild/cover.out\e[0m       Run tests and generate coverage report.\n"
	@printf "  \e[36mbuild/cover.html\e[0m      Generate an HTML file with source code annotations from the coverage report.\n"
	@printf "\n"
	@printf "\e[1mDevelopment\e[0m\n"
	@printf "  \e[36mvendor\e[0m                Run go mod tidy, go mod verify, and go mod vendor.\n"
	@printf "  \e[36mvendor-compat\e[0m         Same as 'make vendor' but go mod tidy will use '-compat' flag with the Go version from go.mod file as value.\n"
	@printf "  \e[36mlicense-headers\e[0m       Add license headers to all .go files excluding the vendor directory.\n"
	@printf "  \e[36mclean\e[0m                 Run git clean.\n"

.PHONY: FORCE
