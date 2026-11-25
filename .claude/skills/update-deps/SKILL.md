---
name: update-deps
description: Updates every dependency of bytekodex-telegram to its newest stable version, Go, Go modules, the platform release, Docker images, and GitHub Actions, proves the bot still works, and commits. The compilers the bot offers move through /update-toolchains instead. Use when the owner asks to update dependencies, libraries, or tools.
argument-hint: "[only these, e.g. go platform]"
---

# Update dependencies

Bring every pinned version in this project up to the newest stable one, prove the project still works, and commit. The same file sits in every Bytekodex repository, each with its own list; this project's own facts live in its CLAUDE.md.

If the owner named only some dependencies (`$ARGUMENTS`), update just those.

## The one limit: the compilers

The JDKs, Kotlin, and Groovy versions in `toolchains/manifest.json` stay as they are: `/update-toolchains` moves them. The base image of the toolchain images (`debian` in `toolchains/Dockerfile.jdk`) is a dependency, but a new tag only reaches images built after it: `deploy.sh` skips every tag the registry already has.

## Steps

1. **Read CLAUDE.md.** Its “Versions” section lists where versions are pinned, its “Checking a change” section says how to prove the project works. Then run `git status`: leave other people's uncommitted changes alone and keep them out of your commits.
2. **List every pinned version:** `go.mod` (its `go`, `toolchain`, and `require` lines), `PLATFORM_VERSION`, `FROM` lines of `Dockerfile.bot` and `toolchains/Dockerfile.*`, `go-version` and `uses:` in `.github/workflows/`.
3. **Find each newest version** where `sources.md` (next to this file) says to ask. Note old and new side by side. If nothing is behind, say so and stop: no commit, no changelog, no release.
4. **Update in groups:** Go, Go modules, the platform, images, Actions. Build and run the checks after each group, so a failure points at its cause. Rust in `Dockerfile.bot` follows the platform's `rust-toolchain.toml` at the tag in `PLATFORM_VERSION`, never runs ahead of it.
5. **Majors:** read the release notes or migration guide and fix the code. If a major breaks too much to fix well, keep the old major and say so in the report.
6. **Check** as CLAUDE.md's “Checking a change” says, all of it.
7. **Changelog.** When the update reaches the bot's image (Go, a module, the platform, a `FROM` line of `Dockerfile.bot`), add a “## X.Y.Z” section to CHANGELOG.md with the next patch version, or add a line to the topmost section if it isn't released yet (`gh release view vX.Y.Z` fails). Updates that only touch CI don't need one.
8. **Commit and push,** one commit per group, in the project's commit style: “Update Go to 1.27.5 and go-telegram/bot to 1.31.0”. Release when the owner asks or when the autopilot runs you (its workflow says so).

## Running unattended

The autopilot (`.github/workflows/autopilot.yml`) runs this every Monday in GitHub Actions, with nobody to answer. Where a step says to ask the owner, take the choice that keeps the bot as it works today, and name it in the report. A failure you can't fix stays out of the commit: keep the old version and go on with the rest.

## Report

A short table: each dependency, old version, new version. Then what stayed behind and why, and how the check went.
