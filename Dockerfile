# @note all COPY commands are restricted to list of files in .dockerignore file.

# We want to fail if arguments were not passed.
# This value is passed either from CI pipeline or makefile (via bash).
ARG GO_VERSION=INVALID

# For build stage we use standard debian version of image.
# --platform=$BUILDPLATFORM ensures cross-compilation using `go build` instead of QEMU.
# @see https://www.docker.com/blog/faster-multi-platform-builds-dockerfile-cross-compilation-guide/
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS builder

# SOURCE_DATE_EPOCH helps with reproducible builds by making build timestamp deterministic.
# @see https://reproducible-builds.org/docs/source-date-epoch/
ARG SOURCE_DATE_EPOCH=0
ENV SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH}

# Passed by buildx for cross-compilation.
ARG TARGETOS
ARG TARGETARCH

# FROM resets arguments, so we need to declare them after.
ARG COMMIT_HASH
ARG RELEASE_TAG

WORKDIR /tmp/build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,id=gomodcache go mod download

COPY . .

# CGO disabled by default.
# Any build that requires CGO will need to adjust build process:
#   * pre-install dependancies for builder stage which are required for build
#   * install runtime dependancies for packaging stage
ENV CGO_ENABLED=0

# GOGC during compilation.
# Default is GOGC=100.
# Higher values reduce frequency of garbage collection, potentially reducing compilation time,
# but increasing memory consumption.
ENV GOGC=100

# Build.
#
# `docker buildx` provides TARGETOS and TARGETARCH for cross-compilation. These values
# must be passed to the Go toolchain explicitly as GOOS and GOARCH.
#
# -trimpath removes file system paths from the binary, improves build reproducibility.
#
# -buildvcs=false removes vcs information from the binary, improves build reproducibility.
#
# -s -w strips debugging data from binary, reducing its size, but makes debugging more complicated.
# Specifically, line numbers, paths and some panic information will be missing. Systems, like Sentry,
# will not be able to provide detailed insights because of that.
#
# xBuild... are variables accessable in main.go
#
RUN --mount=type=cache,target=/go/pkg/mod,id=gomodcache \
    --mount=type=cache,target=/root/.cache/go-build,id=gobuildcache \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -v -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.xBuildCommit=${COMMIT_HASH} -X main.xBuildTag=${RELEASE_TAG}" \
    -o app cmd/app/main.go

# Validate binary.
RUN readelf -h app && du -h app && sha256sum app && go tool buildid app

# ---

# For packaging stage, we use minimal(slim) image.
# This reduces resulting image size and potential security risks.
# @warn dependabot will update image version automatically, but it will not update package versions.
# @note wlays pid sha256 digest for alpine image to avoid accidental updates and potential security risks.
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

# Install dependencies.
#   * ca-certificates - required for https requests
#   * tzdata - required for time zone operations
#   * tini - proper signal handling for child processes
#   * curl - required for docker health checks in ci/cd workflows
#   * libcrypto3, libssl3 - patched OpenSSL runtime libraries
#
# Check for versions @ https://pkgs.alpinelinux.org/packages?branch=v3.24
# When updating image version, make sure to re-check package availability and versions
# for that specific alpine version you are updating to.
RUN apk add --no-cache \
    ca-certificates=20260611-r0 \
    curl=8.22.0-r0 \
    libcrypto3=3.5.8-r0 \
    libssl3=3.5.8-r0 \
    tini=0.19.0-r3 \
    tzdata=2026c-r0

# We are running service as non-root user.
RUN addgroup -g 1000 appuser && \
    adduser -u 1000 -G appuser -s /bin/sh -D appuser

# Copy binary and other files from builder stage.
COPY --from=builder --chown=appuser:appuser /tmp/build/app /usr/local/bin/
COPY --chown=appuser:appuser api/openapi /usr/share/www/api
COPY --chown=appuser:appuser static /usr/share/www
COPY --chown=appuser:appuser migrate /migrate
COPY --chown=appuser:appuser .env.example /

# Entry point for container:
#   * tini is a small init system that helps with proper signal handling and reaping zombie processes.
#   * entrypoint.sh allows to run arbitrary commands and exec inside running containers.
COPY entrypoint.sh /
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/sbin/tini", "--", "/entrypoint.sh"]

# Application will be started by appuser inside isolated home directory.
USER 1000
WORKDIR /home/appuser
CMD ["/usr/local/bin/app"]
