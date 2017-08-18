FROM golang:1.8.3-alpine3.6 as builder
WORKDIR /x/src/github.com/sapcc/limes/
RUN apk add --no-cache make

COPY . .
RUN make install PREFIX=/pkg

################################################################################

FROM alpine:latest
MAINTAINER "Stefan Majewsky <stefan.majewsky@sap.com>"

ENTRYPOINT ["/usr/bin/limes"]
COPY --from=builder /pkg/ /usr/
