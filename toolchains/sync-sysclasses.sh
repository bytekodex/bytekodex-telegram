#!/usr/bin/env bash
# Extracts every locked JDK's system classes onto the host, at the path the bot reads them from.
# The bot itself never touches Docker for this — it is a plain file read — so what has to happen
# once, here, is turning each `sysclasses:<major>` image into a directory of .class files.
#
# Usage: toolchains/sync-sysclasses.sh [destination]
set -euo pipefail
cd "$(dirname "$0")/.."

dest="${1:-/opt/bytekodex/sysclasses}"
mkdir -p "${dest}"

while read -r major; do
  if [ -d "${dest}/${major}/java" ]; then
    echo "skip  ${major} (already on disk)"
    continue
  fi

  tag="ghcr.io/bytekodex/sysclasses:${major}"
  echo "sync  ${major}"
  docker pull -q "${tag}"

  # A scratch container is the only way to read a file out of an image: `docker cp` needs a
  # container, even one that never runs anything (CMD is unset in Dockerfile.sysclasses on purpose).
  container="$(docker create "${tag}" /java)"
  mkdir -p "${dest}/${major}"
  docker cp -q "${container}:/java" "${dest}/${major}/java"
  docker rm -f "${container}" >/dev/null
done < <(go run ./cmd/toolchainctl majors)

echo "sysclasses on disk: $(find "${dest}" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ') version(s)"
