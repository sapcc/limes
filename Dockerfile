FROM golang:1.12-alpine as builder
WORKDIR /x/src/github.com/sapcc/limes/
RUN apk add --no-cache make gcc musl-dev

COPY . .
RUN make install PREFIX=/pkg

################################################################################

FROM alpine:latest
MAINTAINER "Stefan Majewsky <stefan.majewsky@sap.com>"

RUN apk add --no-cache ca-certificates

ENTRYPOINT ["/usr/bin/limes"]
COPY --from=builder /pkg/ /usr/
