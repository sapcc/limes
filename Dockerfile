FROM golang:1.14-alpine as builder
RUN apk add --no-cache make gcc musl-dev

COPY . /src
RUN make -C /src install PREFIX=/pkg GO_BUILDFLAGS='-mod vendor'

################################################################################

FROM alpine:latest
LABEL source_repository="https://github.com/sapcc/limes"

RUN apk add --no-cache ca-certificates
COPY --from=builder /pkg/ /usr/
ENTRYPOINT ["/usr/bin/limes"]
