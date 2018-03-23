#!/bin/bash
set -euo pipefail

if docker inspect schwift-testing &>/dev/null; then
  echo 'Already running.'
else
  # The `readlink -f` converts the path to repo/testing/data to an absolute path.
  DATA_PATH="$(readlink -f "$(dirname $0)")/data"
  if [ ! -d "${DATA_PATH}" ]; then
    mkdir "${DATA_PATH}"
    chown 1000:1000 "${DATA_PATH}"
  fi

  exec docker run --name schwift-testing -P -v "${DATA_PATH}:/swift/nodes" -t bouncestorage/swift-aio
fi
