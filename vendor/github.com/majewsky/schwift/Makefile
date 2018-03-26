help:
	@echo 'Available targets:'
	@echo '    make generate'
	@echo '    make test'

################################################################################

generate: generated.go

%: %.in | util/render_template.go
	@echo ./util/render_template.go < $< > $@
	@./util/render_template.go < $< > $@.new && mv $@.new $@ || (rm $@.new; false)

################################################################################

test: static-tests cover.html

static-tests: FORCE
	@echo '>> gofmt...'
	@if s="$$(gofmt -s -l $$(find -name \*.go) 2>/dev/null)" && test -n "$$s"; then echo "$$s"; false; fi
	@echo '>> golint...'
	@if s="$$(golint ./... 2>/dev/null)" && test -n "$$s"; then echo "$$s"; false; fi
	@echo '>> govet...'
	@go vet ./...

cover.out: FORCE
	@echo '>> go test...'
	@go test -covermode count -coverpkg github.com/majewsky/schwift/... -coverprofile $@ github.com/majewsky/schwift/tests
cover.html: cover.out
	@echo '>> rendering cover.html...'
	@go tool cover -html=$< -o $@

.PHONY: FORCE
