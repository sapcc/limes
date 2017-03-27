FROM alpine:latest
MAINTAINER "Stefan Majewsky <stefan.majewsky@sap.com>"

ADD build/docker.tar /
ENTRYPOINT ["/usr/bin/limes"]
