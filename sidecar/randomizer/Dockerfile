# ROM Randomizer sidecar. Vendors the official Universal Pokemon Randomizer ZX
# release jar (GPLv3, https://github.com/Ajarmar/universal-pokemon-randomizer-zx)
# rather than reimplementing generation-specific ROM randomization logic, and
# wraps it with a small JDK-stdlib-only HTTP shim (Shim.java, no framework).
# Reachable only on the internal compose network, never published to the host.

FROM eclipse-temurin:21-jdk-jammy AS build
WORKDIR /src

# Pinned upstream release: v4.6.1. Bumping this requires re-verifying the
# SHA-256 below (upstream does not publish per-asset checksums) and re-checking
# Shim.java's setter calls still match Settings.java's public API.
ARG RANDOMIZER_VERSION=v4.6.1
ARG RANDOMIZER_ZIP_URL=https://github.com/Ajarmar/universal-pokemon-randomizer-zx/releases/download/v4.6.1/PokeRandoZX-v4_6_1.zip
ARG RANDOMIZER_ZIP_SHA256=26051fe8a6665ea0582dfcd4d1c3e4889da42a2f731f5fc530fff0cb691a43fd
ARG JSON_JAR_URL=https://repo1.maven.org/maven2/org/json/json/20240303/json-20240303.jar
ARG JSON_JAR_SHA256=3cf6cd6892e32e2b4c1c39e0f52f5248a2f5b37646fdfbb79a66b46b618414ed

RUN apt-get update && apt-get install -y --no-install-recommends unzip curl && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL -o rando.zip "$RANDOMIZER_ZIP_URL" \
    && echo "$RANDOMIZER_ZIP_SHA256  rando.zip" | sha256sum -c - \
    && unzip -j rando.zip PokeRandoZX.jar -d . \
    && curl -fsSL -o json.jar "$JSON_JAR_URL" \
    && echo "$JSON_JAR_SHA256  json.jar" | sha256sum -c -

COPY Shim.java .
RUN javac -cp PokeRandoZX.jar:json.jar -d classes Shim.java

FROM eclipse-temurin:21-jre-jammy
WORKDIR /app
COPY --from=build /src/PokeRandoZX.jar /src/json.jar ./
COPY --from=build /src/classes ./classes
ENV RANDOMIZER_PORT=8083
EXPOSE 8083
ENTRYPOINT ["java", "-cp", "classes:PokeRandoZX.jar:json.jar", "Shim"]
