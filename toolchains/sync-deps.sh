#!/usr/bin/env bash
# Seeds the jars every Kotlin compilation puts on its classpath — see Toolchain.Classpath in
# internal/toolchain/catalog.go. Kotlin's binary metadata is one-way compatible (a compiler reads
# metadata from its own version or older, never newer), so one fixed pair of jars cannot serve
# manifest.json's full 1.2.71-to-2.4.20 range: these versions are pinned to what the newest,
# default toolchain needs. Kotlin 1.2-1.5 predate stable kotlinx.coroutines outright, so `suspend
# fun` on those releases was never going to work off this classpath either way — a deliberate,
# discussed limitation, not an oversight.
#
# Usage: toolchains/sync-deps.sh [destination]
set -euo pipefail

dest="${1:-/opt/bytekodex/deps}"
mkdir -p "${dest}"

fetch() {
  local group="$1" artifact="$2" version="$3" out_name="$4" sha1="$5"
  local out="${dest}/${out_name}"
  if [ -f "${out}" ] && echo "${sha1}  ${out}" | sha1sum -c - >/dev/null 2>&1; then
    echo "skip  ${out_name} (already correct)"
    return
  fi
  local url="https://repo1.maven.org/maven2/${group//./\/}/${artifact}/${version}/${artifact}-${version}.jar"
  echo "fetch ${out_name} (${artifact} ${version})"
  curl -fsSL --max-time 60 "${url}" -o "${out}.tmp"
  echo "${sha1}  ${out}.tmp" | sha1sum -c -
  mv "${out}.tmp" "${out}"
}

# Real digests published as the .jar.sha1 sidecar next to each artifact on Maven Central — never
# invented, never trusted from anywhere that isn't the artifact's own repository.
fetch org.jetbrains.kotlin    kotlin-stdlib              2.4.20 kotlin-stdlib.jar              94c1451890d164aed65d43b3db0b7ecef7c183e7
fetch org.jetbrains.kotlinx   kotlinx-coroutines-core-jvm 1.11.0 kotlinx-coroutines-core-jvm.jar 3d57dc678bd8d72a60e7adf0eca5c54e3f4f4b79

echo "deps on disk: $(ls "${dest}")"
