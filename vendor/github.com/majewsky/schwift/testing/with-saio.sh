#!/bin/sh

if ! docker inspect schwift-testing >/dev/null; then
  echo "SAIO container not running yet. Run ./testing/start-saio.sh to start it." >&2
  exit 1
fi

ST_PORT="$(docker inspect schwift-testing | jq -r '.[0].NetworkSettings.Ports["8080/tcp"][0].HostPort')"
export ST_AUTH="http://127.0.0.1:${ST_PORT}/auth/v1.0"
export ST_USER="test:tester" # hardcoded auth parameters in the bouncestorage/docker-swift image
export ST_KEY="testing"

exec "$@"
