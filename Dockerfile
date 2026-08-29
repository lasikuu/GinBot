# syntax=docker/dockerfile:1

# --platform=$BUILDPLATFORM keeps the toolchain native and cross-compiles;
# without it buildx runs the compiler under QEMU for every arch.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build-base
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# TARGETOS/TARGETARCH are BuildKit-populated build args.
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}

# One stage per binary so a given --target only compiles the binary it ships.
FROM build-base AS build-server
RUN go build -o /out/ginbot-server ./cmd/ginbot-server

FROM build-base AS build-discord
RUN go build -o /out/ginbot-discord ./cmd/ginbot-discord

FROM build-base AS build-matrix
RUN go build -o /out/ginbot-matrix ./cmd/ginbot-matrix

# WORKDIR /app: internal/auth resolves cert/*.pem relative to the working dir.
# No --platform: this stage is the target arch, so apk runs under QEMU on
# cross builds.
FROM alpine:3.20 AS runtime-base
RUN apk add --no-cache ca-certificates
WORKDIR /app

FROM runtime-base AS server
COPY --from=build-server /out/ginbot-server /usr/local/bin/
ENTRYPOINT ["ginbot-server"]

FROM runtime-base AS discord
COPY --from=build-discord /out/ginbot-discord /usr/local/bin/
ENTRYPOINT ["ginbot-discord"]

FROM runtime-base AS matrix
COPY --from=build-matrix /out/ginbot-matrix /usr/local/bin/
ENTRYPOINT ["ginbot-matrix"]
