---
name: update-toolchains
description: Adds the newest JDK, Kotlin, and Groovy releases to the compilers @bytekodex_bot offers, moves each line to its newest patch, resolves the lock, builds the images, and commits. Use when the owner asks for a new Java, Kotlin, or Groovy version, or for fresh compilers.
argument-hint: "[only these, e.g. kotlin]"
---

# Update the toolchains

The bot offers every JDK from 7 up, one Kotlin per minor release since 1.2, and one Groovy per minor release since 2.4. This skill keeps that list current: a new release joins, a newer patch replaces the old one of its line, and a JDK leaves early access once it has a GA build. The rules for each field are in the comment at the top of `toolchains/manifest.json` and in `internal/toolchain/manifest.go`.

If the owner named only some languages (`$ARGUMENTS`), update just those.

## Steps

1. **Read CLAUDE.md** and run `git status`, as `/update-deps` says.
2. **Find what's new** where `.claude/skills/update-deps/sources.md` says to ask: JDK majors on foojay, Kotlin releases, Groovy releases, kotlinx-coroutines.
3. **Edit `toolchains/manifest.json`:**
   - **JDK.** A new major with only `ea` builds joins with `"allow_early_access": true`, and it alone gets `"preview": true`: move `preview` off the major that had it. A major whose builds turned `ga` loses `allow_early_access`. `release_floor` copies the previous major's unless the new javac's `--release` help says otherwise.
   - **Kotlin and Groovy.** A newer patch replaces its line's entry (2.4.20 becomes 2.4.21), a new minor or major adds one. Find `jvm_target_max` from the compiler itself, as sources.md says. `jdk` is the JDK image the compiler runs on: the newest LTS it supports, or the newest GA JDK when it targets past the LTS. A Kotlin entry names the newest kotlinx-coroutines it can read.
   - **Default.** `"default": true` marks the newest stable release of each language.
4. **Resolve:** `GITHUB_TOKEN=$(gh auth token) go run ./cmd/toolchainctl resolve`. Run it even when the manifest didn't change: it also moves every JDK to its newest build. If `toolchains/lock.json` didn't change either, stop: no commit, no changelog, no release.
5. **Check** as CLAUDE.md's “Checking a change” says. `go run ./cmd/toolchainctl show` must list every version you added.
6. **Changelog.** Add what users get to the topmost unreleased “## X.Y.Z” section of CHANGELOG.md, or open the next minor version for a new language version, the next patch for patch builds alone.
7. **Commit and push** `manifest.json` and `lock.json` together: “Offer Kotlin 2.5.0 and JDK 29”. A push made with the autopilot's token starts no workflow, so start the images by hand: `gh workflow run toolchains.yml`, then `gh run watch` until it builds and pushes every new image. The release builds whatever is still missing anyway, but a failed image shows up here first.

## Running unattended

The autopilot runs this after `/update-deps` every Monday. A version whose image fails to build stays out: take it back out of the manifest, resolve again, and say so in the report.

## Report

A short table: each language, what joined, what moved, what left early access. Then what stayed behind and why.
