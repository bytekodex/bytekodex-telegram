# Bytekodex for Telegram: notes for AI agents

The Telegram bot [@bytekodex_bot](https://t.me/bytekodex_bot): send it Java, Kotlin, or Groovy, and it compiles the code in a locked-down container and answers with pictures of the bytecode. It is Go on top of bytekodex/bytekodex-platform, which it links through cgo over a C ABI. README.md is the shop window (what the bot gives, how to start, links), README.RU.md its Russian copy, CHANGELOG.md what each release brings. The developer side (layout, deploying, running it yourself) lives in docs/development.md.

## Talking to the owner

- Reply in Russian, address the owner as «вы». Quotes «ёлочки», nested „лапки“, an em dash with spaces « — ».
- Code, comments, commit messages, CHANGELOG.md, and README.md are in English: curly quotes “…”, an en dash with spaces (“text – text”), never an em dash glued to the words. No semicolon between clauses: a full stop, a comma, or a colon instead. README.RU.md follows Russian typography. Change both READMEs together.
- Commits are short one-line imperatives, one logical change each: “Offer Kotlin 2.5.0”.

## How it fits together

| Path | What it does |
|---|---|
| `internal/detect` | Which language is this: a weighted guess, never a blocking question |
| `internal/toolchain` | The catalog of languages, compiler versions, bytecode targets, and images, built from `toolchains/lock.json`; the resolver that writes that lock from `manifest.json` |
| `internal/compile` | Runs a compiler in a throwaway container (no network, read-only root, no capabilities, pid, memory, and time caps) and reads the classes back |
| `internal/render` | cgo binding to the platform, with a vendored copy of `bytekodex.h` |
| `internal/session` | Per-chat state, in memory, expiring |
| `internal/sysclass` | The JDK's own classes, for a message that is just a class name |
| `internal/ui` | The keyboard and its callback encoding |
| `internal/bot` | Handlers |
| `cmd/bot` | The bot's entry point |
| `cmd/toolchainctl` | `resolve`, `plan`, `show`, `probe`, and `sync`: the toolchain lock and the host the bot runs on |
| `toolchains/` | `manifest.json` (what a human decides), `lock.json` (what the resolver found), and the Dockerfiles of the compiler images |

## Rules that keep it working

- **Compiling is running a stranger's code.** Groovy runs AST transformations and `@Grab` at compile time, javac runs annotation processors, kotlinc loads plugins. Every compilation stays in its container with the limits in `compile.DefaultLimits`, and javac keeps `-proc:none`. Never loosen them for a feature.
- **The image never touches disk.** Rust writes the PNG into a pooled Go slice, the upload streams it from there. Pages go out as documents, never photos: Telegram recompresses photos to JPEG.
- **The header is the platform's.** `internal/render/include/bytekodex.h` is a copy of `include/bytekodex.h` at the tag in `PLATFORM_VERSION`, and CI fails when they differ. `render.New` refuses a library with another `bk_abi_version`.
- **`lock.json` comes from the resolver,** never from a hand edit: `go run ./cmd/toolchainctl resolve`.
- **The bot's texts are English.** Buttons and messages live in `internal/ui` and `internal/bot`.

## Versions

- Go: `toolchain` in `go.mod`, `golang:<version>-trixie` in `Dockerfile.bot`, and `go-version` in `ci.yml`, `release.yml`, `toolchains.yml`, and `autopilot.yml`, all the same. The `go` line is the oldest Go the code needs.
- Go modules: `go.mod` and `go.sum`.
- The platform: `PLATFORM_VERSION`, a released tag without its `v`. Rust in `Dockerfile.bot` matches that tag's `rust-toolchain.toml`.
- Images: the `FROM` lines of `Dockerfile.bot` (rust, golang, docker CLI, debian) and `BASE` in `toolchains/Dockerfile.jdk`, each an exact tag.
- Compilers: `toolchains/manifest.json`. `/update-toolchains` moves them, `/update-deps` everything else.
- Actions: `uses:` in `.github/workflows/`.

## Checking a change

The bot links the platform's library. In the superproject it sits at `../platform`. Anywhere else, clone the release first: `git clone --depth 1 --branch v$(cat PLATFORM_VERSION) https://github.com/bytekodex/bytekodex-platform.git ../platform`.

```shell
(cd ../platform && cargo build --release -p bk-ffi)
diff ../platform/include/bytekodex.h internal/render/include/bytekodex.h
gofmt -l .
CGO_LDFLAGS=-L$PWD/../platform/target/release go vet ./...
CGO_LDFLAGS=-L$PWD/../platform/target/release LD_LIBRARY_PATH=$PWD/../platform/target/release go test -race ./...
go run ./cmd/toolchainctl show
```

On macOS use `DYLD_LIBRARY_PATH` instead of `LD_LIBRARY_PATH`. Renderer tests skip without a monospace face, so on Linux install `fonts-dejavu-core` first. The container tests run only where a Docker daemon is up.

## Releasing

- After a change that reaches the bot's image (code, `Dockerfile.bot`, the lock, the platform), add it to CHANGELOG.md in the same push: under the topmost “## X.Y.Z” section while that version isn't released (`gh release view vX.Y.Z` fails), or under a new section with the next version. New compilers or a new feature are a minor version, patch builds, fixes, and updates a patch.
- Release: `gh workflow run release.yml -R bytekodex/bytekodex-telegram -f version=X.Y.Z`. It tests the bot against the platform release in `PLATFORM_VERSION`, builds every toolchain image the lock names that the registry lacks, pushes `ghcr.io/bytekodex/bot:X.Y.Z`, and publishes the `vX.Y.Z` release. Its `deploy` job starts `ship.yml` in MairwunNx/infrastructure with `FAMILY_ACTIONS_TOKEN`: that pins `stacks/bytekodex/compose.yaml` to the version, and the push deploys it.
- Check after a release: the run succeeded, `gh release view vX.Y.Z` exists, MairwunNx/infrastructure got a commit “Run bytekodex on bot X.Y.Z”, and the bot answers `/start`.
- `toolchains.yml` builds new compiler images when `manifest.json` changes on master. A push with the autopilot's token starts no workflow, so the autopilot runs it with `gh workflow run toolchains.yml`.

## Media

`media/` holds what the READMEs show: `hero.webp` (rendered from `media/hero/hero.html`, as its comment says), screenshots of the chat, and the recorded scenes as `.mp4` with a `.gif` cut for GitHub, which plays only GIFs inline. The videos are in Git LFS (`.gitattributes`). Record the chat with the Telegram window alone in the frame, never the chat list.

## Autopilot

`.github/workflows/autopilot.yml` runs Claude every Monday at 05:00 UTC with `CLAUDE_CODE_OAUTH_TOKEN`: `/update-deps`, `/update-toolchains`, then a release when the image changed. The platform's autopilot runs at 03:00, so a new platform release is there to move to. The secrets come from MairwunNx/infrastructure (`github.toml`, `scripts/secrets.py github`).
