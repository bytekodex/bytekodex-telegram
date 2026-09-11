#!/usr/bin/env bash
# Seeds the classpath jars every locked Kotlin release needs — see Release.Classpath in
# internal/toolchain/catalog.go. Kotlin's class metadata is forward-readable only, so this is
# per-version, not a single shared pair: which coroutines/stdlib jars a given Kotlin release gets
# is decided once, in toolchains/manifest.json's "coroutines" field, and resolved with a real
# checksum by `toolchainctl resolve` — nothing here is hardcoded or guessed.
#
# Usage: toolchains/sync-deps.sh [destination]
set -euo pipefail
cd "$(dirname "$0")/.."

dest="${1:-/opt/bytekodex/deps}"
mkdir -p "${dest}"

while IFS=$'\t' read -r version name url sha256; do
  out="${dest}/${version}/${name}"
  if [ -f "${out}" ] && echo "${sha256}  ${out}" | sha256sum -c - >/dev/null 2>&1; then
    echo "skip  ${version}/${name} (already correct)"
    continue
  fi
  echo "fetch ${version}/${name}"
  mkdir -p "${dest}/${version}"
  curl -fsSL --max-time 60 "${url}" -o "${out}.tmp"
  echo "${sha256}  ${out}.tmp" | sha256sum -c -
  mv "${out}.tmp" "${out}"
done < <(go run ./cmd/toolchainctl deps)

echo "deps on disk: $(find "${dest}" -mindepth 2 -type f | wc -l | tr -d ' ') jar(s) across $(find "${dest}" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ') version(s)"
