FROM alpine:latest
MAINTAINER "Stefan Majewsky <stefan.majewsky@sap.com>"
ENTRYPOINT ["/usr/bin/limes"]

ADD build/docker.tar /
