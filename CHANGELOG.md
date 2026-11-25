# Changelog

The “Release” workflow takes the notes for a version from its section here, unless you type them into its `changelog` field.

## 1.0.0

- 🎉 **The first release.** Send [@bytekodex_bot](https://t.me/bytekodex_bot) Java, Kotlin, or Groovy, press Compile, and get one picture per class: methods, the constant pool, locals, the stack map, and how many opcodes it all took. A message that is just a JDK class name, like `java.lang.String`, shows the bytecode the JDK ships.
- 🟢 **Groovy 6.0.0** joins and is the new default. It runs on JDK 27 and emits bytecode up to Java 27. Groovy 5.1 moves to 5.1.3.
- 🟣 **Kotlin 2.4.21** replaces 2.4.20 and emits bytecode up to Java 26.
- ☕ **JDK 27 is final.** The bot compiles with its GA build instead of early access, and every other JDK moves to its newest build. JDK 28 stays in early access, with preview features on.
- 🏷️ **A new JDK build is a new image.** JDK images were tagged by their major alone, so a newer build never replaced the one in the registry. Each image now carries a digest of its archives, and the server swaps in the system classes of the new build.
- 🚀 **Releases.** The bot ships as `ghcr.io/bytekodex/bot:<version>` when a release runs, not on every push, and links against a released platform (`PLATFORM_VERSION`).
- ⬆️ Updated Go to 1.27.4, go-telegram/bot to 1.30.0, the docker CLI to 29.10.0, and pinned every base image to an exact tag.
