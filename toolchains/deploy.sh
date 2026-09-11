#!/usr/bin/env bash
# Builds and pushes every toolchain image the lock implies, skipping any tag the registry already
# has. A version's checksum is fixed once resolved, so an existing tag is byte-for-byte what a
# rebuild would produce anyway — the only versions worth spending CI minutes on are new ones.
#
# Usage: toolchains/deploy.sh [platform]
#   platform defaults to amd64, which is what the production host runs. Building the other
#   architecture too would mean paying for QEMU emulation on every image for something nothing
#   ever pulls; do that from a laptop with `toolchainctl plan` directly instead.
set -euo pipefail
cd "$(dirname "$0")/.."

platform="${1:-amd64}"
built=0
skipped=0

# toolchainctl plan prints one valid multi-line `docker build ... toolchains` invocation after
# another, back to back, with no separator — which is also exactly the shape `sh` wants. Splitting
# on the `-t <tag>` line is what lets each block be checked and pushed on its own.
current=""
tag=""

flush() {
  if [ -z "${current}" ]; then
    return
  fi
  if docker manifest inspect "${tag}" >/dev/null 2>&1; then
    echo "skip  ${tag} (already in the registry)"
    skipped=$((skipped + 1))
    return
  fi
  echo "build ${tag}"
  echo "${current}" | sh
  docker push "${tag}"
  built=$((built + 1))
}

while IFS= read -r line; do
  if [[ "${line}" == "docker build "* ]]; then
    flush
    current="${line}"
    tag="$(echo "${line}" | grep -oE '\-t [^ ]+' | cut -d' ' -f2)"
  else
    current="${current}"$'\n'"${line}"
  fi
done < <(go run ./cmd/toolchainctl plan -platform "${platform}")
flush

echo "${built} built and pushed, ${skipped} already present"
