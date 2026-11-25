# Development

How the bot is put together, how it ships, and how to run it yourself. The [README](../README.md) is for people who use the bot.

## Layout

| Package | Role |
| --- | --- |
| `internal/detect` | Which language is this? A weighted guess, never a blocking question |
| `internal/toolchain` | The catalog: languages, compiler versions, bytecode targets, images |
| `internal/compile` | Runs a compiler in a locked-down container and reads the classes back |
| `internal/render` | cgo binding to the Rust platform over its C ABI |
| `internal/session` | Per-chat state, in memory, expiring |
| `internal/sysclass` | The JDK’s own classes, for a message that is just a class name |
| `internal/ui` | The keyboard and its callback encoding |
| `internal/bot` | Handlers |
| `cmd/bot` | Entry point |
| `cmd/toolchainctl` | The toolchain lock, the image plan, and the host sync |

## The image never touches disk

Rust writes the PNG into a Go byte slice that came from a pool, and the Telegram client streams the upload straight out of that same slice. The bytes are written once and read once, there is no temporary file to clean up, and the buffer goes back to the pool when the upload finishes.

The buffer belongs to Go, so nothing crosses the boundary owning memory. If a page doesn’t fit, the library reports the exact size it needs, and one retry is always enough.

Pages go out as documents rather than photos. `sendPhoto` re-encodes on Telegram’s servers, and JPEG on small sharp glyphs is the worst case for the one thing this bot is for. A document keeps the bytes exactly as rendered, and the width-plus-height cap of 10 000 doesn’t apply to it.

## Compiling is running

Compiling source from a stranger executes their code. Groovy runs global AST transformations and `@Grab` at compile time, `javac` runs any annotation processor it finds, and `kotlinc` loads compiler plugins. So every compilation happens in a throwaway container with no network, a read-only root, an empty capability set, a pid cap, a memory cap, and a deadline. `javac` also gets `-proc:none`.

## Toolchains

`toolchains/manifest.json` holds what a human decides: which JDKs, Kotlin, and Groovy versions the bot offers. `go run ./cmd/toolchainctl resolve` looks up every download and checksum and writes `toolchains/lock.json`. Each version becomes an image: `ghcr.io/bytekodex/jdk:<major>-<digest>`, `ghcr.io/bytekodex/kotlin:<version>`, `ghcr.io/bytekodex/groovy:<version>`, and the JDK’s own classes as `ghcr.io/bytekodex/sysclasses:<major>-<digest>`. The digest covers the JDK archives, so a new build of a major is a new image.

`toolchains.yml` builds the images a new manifest needs as soon as it lands on master. `toolchains/deploy.sh` builds and pushes only the tags the registry doesn’t have.

## Releasing

Run **Actions → Release → Run workflow** with the new version, or:

```shell
gh workflow run release.yml -R bytekodex/bytekodex-telegram -f version=1.2.0
```

It takes the notes from the version’s section of [CHANGELOG.md](../CHANGELOG.md), tests the bot against the platform release in `PLATFORM_VERSION`, builds any toolchain image still missing, pushes `ghcr.io/bytekodex/bot:<version>`, and publishes the GitHub release. Then it starts `ship.yml` in the infrastructure repository, which pins the server’s stack to the new version, and the push deploys it. That needs one secret besides the automatic `GITHUB_TOKEN`: `FAMILY_ACTIONS_TOKEN`.

Before the bot starts, the server runs `toolchainctl sync` from the same image. It pulls the image of every compiler the lock offers, extracts each JDK’s system classes, downloads the Kotlin classpath jars, makes the scratch directory, and removes compiler images the lock no longer names. Whatever is already there is skipped.

Every Monday the autopilot (`autopilot.yml`) runs Claude with `/update-deps` and `/update-toolchains`, and releases what changed.

## Running it on your own machine

The bot starts its compilers through a Docker socket, and the daemon behind it resolves every `--volume` path the bot hands it. So the data directory sits at the same path on the host and in both containers. Take the newest version from [Releases](https://github.com/bytekodex/bytekodex-telegram/releases):

```yaml
services:
  sync:
    image: ghcr.io/bytekodex/bot:1.0.0
    user: "0:0"   # to chown the scratch directory to the bot's user
    entrypoint: [toolchainctl, sync, -dir, /opt/bytekodex, -root, /srv/bytekodex]
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /srv/bytekodex:/srv/bytekodex

  bot:
    image: ghcr.io/bytekodex/bot:1.0.0
    restart: unless-stopped
    depends_on: { sync: { condition: service_completed_successfully } }
    group_add: ["999"]   # the group that owns the socket: `stat -c %g /var/run/docker.sock`; "0" under rootless Docker
    environment:
      BYTEKODEX_TELEGRAM_TOKEN: ${TELEGRAM_BOT_TOKEN}
      BYTEKODEX_TOOLCHAIN_LOCK: /opt/bytekodex/lock.json
      BYTEKODEX_SYSCLASSES: /srv/bytekodex/sysclasses
      BYTEKODEX_DEPS: /srv/bytekodex/deps
      TMPDIR: /srv/bytekodex/scratch
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /srv/bytekodex/scratch:/srv/bytekodex/scratch
      - /srv/bytekodex/deps:/srv/bytekodex/deps:ro
      - /srv/bytekodex/sysclasses:/srv/bytekodex/sysclasses:ro
```

The first `sync` pulls every compiler image and takes a while. Later ones finish in seconds.

## Running it from source

The bot needs the platform’s library, a monospace TrueType face, and a container runtime.

```shell
# Build the library from the platform release in PLATFORM_VERSION first.
export CGO_LDFLAGS="-L/path/to/bytekodex-platform/target/release"
export LD_LIBRARY_PATH="/path/to/bytekodex-platform/target/release"

export BYTEKODEX_TELEGRAM_TOKEN=...
export BYTEKODEX_FONT=/path/to/JetBrainsMono-Regular.ttf
export BYTEKODEX_DEPS=/var/lib/bytekodex/deps   # jars for the Kotlin classpath

go run ./cmd/bot
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `BYTEKODEX_TELEGRAM_TOKEN` | – | Required |
| `BYTEKODEX_FONT` | Fira Code from `fonts-firacode` | A single TrueType face. A `.ttc` is refused |
| `BYTEKODEX_FONT_SIZE` | `40` | Render cost is linear in pixels, and a client scales the image down anyway |
| `BYTEKODEX_DEPS` | – | Directory of jars mounted read-only for the classpath |
| `BYTEKODEX_TOOLCHAIN_LOCK` | `toolchains/lock.json` | Which compilers the keyboard offers |
| `BYTEKODEX_SYSCLASSES` | `/var/lib/bytekodex/sysclasses` | Each JDK’s classes, from `toolchainctl sync`, for a message that is just a class name |
| `BYTEKODEX_CONTAINER_RUNTIME` | `docker` | `podman` works too |
| `BYTEKODEX_SESSION_TTL` | `30m` | How long a pasted snippet is remembered |

The header under `internal/render/include` is a copy of the platform’s. The two projects release independently, so `render.New` checks `bk_abi_version` at startup and refuses to run against a library it doesn’t understand.

## Tests

```shell
go test ./...
```

The renderer tests skip themselves when no monospace face is installed. The compiler tests cover the parts that don’t need a container runtime, which notably includes the check that a filename from a stranger can’t escape the scratch directory.
