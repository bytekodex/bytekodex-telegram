# One Groovy compiler, on top of one of our JDK images.
#
# The Apache binary distribution unpacks to groovy-<version>/, so the directory is discovered
# rather than named — that is the only difference from the Kotlin image.
#
#   docker build -f Dockerfile.groovy -t ghcr.io/bytekodex/groovy:5.1.2 \
#     --build-arg BASE=ghcr.io/bytekodex/jdk:25 --build-arg URL=... --build-arg SHA256=... .

ARG BASE

FROM ${BASE} AS download
ARG URL
ARG SHA256

USER root
RUN set -eux; \
    test -n "${URL}"; test -n "${SHA256}"; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl unzip; \
    curl -fsSL --retry 3 -o /tmp/groovy.zip "${URL}"; \
    echo "${SHA256}  /tmp/groovy.zip" | sha256sum -c -; \
    unzip -q /tmp/groovy.zip -d /tmp/groovy; \
    mv "$(find /tmp/groovy -maxdepth 1 -type d -name 'groovy-*')" /opt/groovy; \
    rm -rf /tmp/groovy.zip /tmp/groovy; \
    test -x /opt/groovy/bin/groovyc

FROM ${BASE}
USER root
COPY --from=download /opt/groovy /opt/groovy

ENV GROOVY_HOME=/opt/groovy
ENV PATH=/opt/groovy/bin:$PATH
ENV JAVA_TOOL_OPTIONS=-Djava.io.tmpdir=/tmp

USER 1000:1000
WORKDIR /work
CMD ["groovyc", "--version"]
