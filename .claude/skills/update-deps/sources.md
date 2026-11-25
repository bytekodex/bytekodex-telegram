# Where fresh versions come from

The same file sits in every Bytekodex repository; change all copies together. Ask the source, never your memory: versions move every week. Take the newest **stable** version. Skip anything marked alpha, beta, rc, pre, M1, snapshot, nightly, ea, or dev, unless the manifest asks for it (`allow_early_access` on a JDK that has no GA build yet).

Compare versions as numbers, part by part: 1.100 is newer than 1.99, and 2.4.21 newer than 2.4.3. A list sorted as text, or a `<latest>` tag, can name an older version.

## Rust

| What | Where to ask | Notes |
|---|---|---|
| Rust | `https://static.rust-lang.org/dist/channel-rust-stable.toml` (`[pkg.rust] version`), or `gh api repos/rust-lang/rust/releases/latest -q .tag_name` | Goes into the platform's `rust-toolchain.toml` and the bot's `Dockerfile.bot` (`rust:<version>-slim-trixie`) together. `rust-version` in `Cargo.toml` is the oldest Rust the code needs: move it only when the code needs a newer one |
| Crates | `cargo update --dry-run`, then `curl -A bytekodex-deps https://crates.io/api/v1/crates/<name>` (`crate.max_stable_version`) | crates.io refuses a request without a User-Agent. `cargo update` stays inside the ranges in `Cargo.toml`: a new major, or a new minor of a 0.x crate, needs its range moved by hand |

## Go

| What | Where to ask | Notes |
|---|---|---|
| Go | `https://go.dev/dl/?mode=json` | The first entry is the newest stable release. It goes into `toolchain go<version>` in `go.mod`, `golang:<version>-trixie` in `Dockerfile.bot`, and `go-version` in every workflow. The `go` line stays at the oldest release the code needs |
| Go modules | `go list -m -u all` lists what's behind | `go get -u ./... && go mod tidy` updates them. A new major is a new module path (`/v2`) and changes the imports |

## The platform

| What | Where to ask | Notes |
|---|---|---|
| bytekodex-platform | `gh release view -R bytekodex/bytekodex-platform --json tagName -q .tagName` | The bot builds the platform from the tag in `PLATFORM_VERSION` (without the `v`). Copy `include/bytekodex.h` of that tag over `internal/render/include/bytekodex.h`. When `BK_ABI_VERSION` changed, the binding needs the new ABI first: that is the owner's change, not an update |

## Toolchains the bot offers

| What | Where to ask | Notes |
|---|---|---|
| JDK majors | `https://api.foojay.io/disco/v3.0/major_versions?ea=true&ga=true&maintained=true` | `release_status` is `ga` or `ea`. A new major shows up as `ea` months before its GA. `toolchainctl resolve` picks each major's newest build by itself |
| Kotlin | `gh api 'repos/JetBrains/kotlin/releases?per_page=30' -q '.[]\|select(.prerelease==false)\|.tag_name'` | One entry per minor release, its newest patch. `kotlinc -help` of the new version prints the `-jvm-target` range: its top is `jvm_target_max` |
| kotlinx-coroutines | `https://repo1.maven.org/maven2/org/jetbrains/kotlinx/kotlinx-coroutines-core/maven-metadata.xml` | The newest stable that the Kotlin version can read. Its release notes name the Kotlin it was built with |
| Groovy 4 and newer | `https://repo1.maven.org/maven2/org/apache/groovy/groovy/maven-metadata.xml` | One entry per minor release, its newest patch. `org.codehaus.groovy.control.CompilerConfiguration.ALLOWED_JDKS` of the new version lists the targets it can emit: its last one is `jvm_target_max` |
| Groovy 2.4 to 3.0 | `https://repo1.maven.org/maven2/org/codehaus/groovy/groovy/maven-metadata.xml` | These lines are finished: 2.4.21, 2.5.23, and 3.0.25 are their last releases |

## Images and Actions

| What | Where to ask | Notes |
|---|---|---|
| Docker Hub images | `https://hub.docker.com/v2/repositories/library/<name>/tags?page_size=100&ordering=last_updated` | `rust`, `golang`, `docker` (the `-cli` tags), `debian` (`trixie-<date>-slim`). Keep the tag's shape |
| GitHub Actions | `gh api repos/<owner>/<action>/releases/latest -q .tag_name` | Pin the major tag (`actions/checkout@v7`) as the workflows already do |
