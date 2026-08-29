# syntax=docker/dockerfile:1

# --- build stage ---
# `--platform=$BUILDPLATFORM` pins the toolchain to the machine actually
# running the build and lets Go cross-compile to the target itself. Without it
# buildx runs the whole compiler under QEMU for every non-native architecture,
# which turns the arm64 image from seconds into minutes.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build-base
WORKDIR /src

# Cache module downloads separately from the source tree.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static binaries so the runtime images can stay minimal. TARGETOS and
# TARGETARCH are built-in build args that BuildKit always populates — the
# `# syntax=` line above requires BuildKit — so this is equally correct for a
# plain single-platform `docker build`.
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}

# One compile stage per binary rather than one chained `go build`. BuildKit
# only executes the stages the selected `--target` depends on, so the discord
# image no longer compiles the server and matrix binaries to discard them —
# which it previously did once per image and, now that there are two
# architectures, would do six times per publish.
FROM build-base AS build-server
RUN go build -o /out/ginbot-server ./cmd/ginbot-server

FROM build-base AS build-discord
RUN go build -o /out/ginbot-discord ./cmd/ginbot-discord

FROM build-base AS build-matrix
RUN go build -o /out/ginbot-matrix ./cmd/ginbot-matrix

# --- shared runtime base ---
# internal/auth resolves cert/*.pem relative to the working directory, so every
# binary must be launched from /app. Mount certs at /app/cert when TLS is on.
#
# No `--platform` here, deliberately: this stage IS the target architecture.
# The single `apk add` below is therefore emulated on a cross build, and is the
# only reason .github/workflows/publish.yml installs QEMU at all.
FROM alpine:3.20 AS runtime-base
# TLS to talk to Discord/Matrix/gRPC peers over HTTPS.
RUN apk add --no-cache ca-certificates
WORKDIR /app

# --- one image per binary; each carries only its own binary ---
FROM runtime-base AS server
COPY --from=build-server /out/ginbot-server /usr/local/bin/
ENTRYPOINT ["ginbot-server"]

FROM runtime-base AS discord
COPY --from=build-discord /out/ginbot-discord /usr/local/bin/
ENTRYPOINT ["ginbot-discord"]

FROM runtime-base AS matrix
COPY --from=build-matrix /out/ginbot-matrix /usr/local/bin/
ENTRYPOINT ["ginbot-matrix"]
