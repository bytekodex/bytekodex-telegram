# bytekodex-telegram

The Telegram bot. Drop in JVM source, get a picture of the bytecode.

## How it behaves

You paste code. The bot says what language it thinks that is and shows a keyboard — it does not
ask you a question you have to answer before anything happens. Press Compile and you get one
image per class.

The keyboard is a single message that rewrites itself:

```
Looks like Kotlin.
Kotlin · Kotlin 2.2 · bytecode 25

[ Language: Kotlin        ]
[ Version: Kotlin 2.2 ] [ Target: 25 ]
[ Show: methods only     ]
[ Compile               ]
[ Discard               ]
```

`Show` toggles the constant pool, the local variable table, the stack map, line numbers and
attributes, which the old bot could not do at all.

## Layout

| Package | Role |
| --- | --- |
| `internal/detect` | Which language is this? A weighted guess, never a blocking question |
| `internal/toolchain` | The catalog: languages, compiler versions, bytecode targets, images |
| `internal/compile` | Runs a compiler in a locked-down container and reads the classes back |
| `internal/render` | cgo binding to the Rust platform over its C ABI |
| `internal/session` | Per-chat state, in memory, expiring |
| `internal/ui` | The keyboard and its callback encoding |
| `internal/bot` | Handlers |
| `cmd/bot` | Entry point |

## The image never touches disk

Rust writes the PNG into a Go byte slice that came from a pool, and the Telegram client streams
the upload straight out of that same slice. The bytes are written once and read once, there is no
temporary file to clean up, and the buffer goes back to the pool when the upload finishes.

The buffer belongs to Go, so nothing crosses the boundary owning memory. If a page does not fit,
the library reports the exact size it needs and one retry is always enough.

Pages go out as documents rather than photos. `sendPhoto` re-encodes on Telegram's servers, and
JPEG on small sharp glyphs is the worst case for the one thing this bot is for. A document keeps
the bytes exactly as rendered, and the width-plus-height cap of 10 000 does not apply to it.

## Compiling is running

Compiling source from a stranger executes their code. Groovy runs global AST transformations and
`@Grab` at compile time, `javac` runs any annotation processor it finds, and `kotlinc` loads
compiler plugins. So every compilation happens in a throwaway container with no network, a
read-only root, an empty capability set, a pid cap, a memory cap and a deadline. `javac` also gets
`-proc:none`.

## Running it

The bot needs the platform's shared library, a monospace TrueType face, and a container runtime.

```shell
# Build the library from the platform repository first.
export CGO_LDFLAGS="-L/path/to/bytekodex-platform/target/release"
export LD_LIBRARY_PATH="/path/to/bytekodex-platform/target/release"

export BYTEKODEX_TELEGRAM_TOKEN=...
export BYTEKODEX_FONT=/path/to/JetBrainsMono-Regular.ttf
export BYTEKODEX_DEPS=/var/lib/bytekodex/deps   # jars for the Kotlin classpath

go run ./cmd/bot
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `BYTEKODEX_TELEGRAM_TOKEN` | — | Required |
| `BYTEKODEX_FONT` | a JetBrains Mono path | Single TrueType face; a `.ttc` is refused |
| `BYTEKODEX_FONT_SIZE` | `28` | Render cost is linear in pixels, and a client scales the image down anyway |
| `BYTEKODEX_DEPS` | — | Directory of jars mounted read-only for the classpath |
| `BYTEKODEX_CONTAINER_RUNTIME` | `docker` | `podman` works too |
| `BYTEKODEX_SESSION_TTL` | `30m` | How long a pasted snippet is remembered |

The header under `internal/render/include` is a vendored copy of the platform's. The two projects
release independently, so `New` checks `bk_abi_version` at startup and refuses to run against a
library it does not understand.

## Tests

```shell
go test ./...
```

The renderer tests skip themselves when no monospace face is installed. The compiler tests cover
the parts that do not need a container runtime, which notably includes the check that a filename
from a stranger cannot escape the scratch directory.

## License

Apache-2.0.
