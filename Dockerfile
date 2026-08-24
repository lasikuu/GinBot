# syntax=docker/dockerfile:1

# --- build stage (compiles all three binaries once) ---
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache module downloads separately from the source tree.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static binaries so the runtime images can stay minimal.
ENV CGO_ENABLED=0
RUN go build -o /out/ginbot-server  ./cmd/ginbot-server \
 && go build -o /out/ginbot-discord ./cmd/ginbot-discord \
 && go build -o /out/ginbot-matrix  ./cmd/ginbot-matrix

# --- shared runtime base ---
# internal/auth resolves cert/*.pem relative to the working directory, so every
# binary must be launched from /app. Mount certs at /app/cert when TLS is on.
FROM alpine:3.20 AS runtime-base
# TLS to talk to Discord/Matrix/gRPC peers over HTTPS.
RUN apk add --no-cache ca-certificates
WORKDIR /app

# --- one image per binary; each carries only its own binary ---
FROM runtime-base AS server
COPY --from=build /out/ginbot-server /usr/local/bin/
ENTRYPOINT ["ginbot-server"]

FROM runtime-base AS discord
COPY --from=build /out/ginbot-discord /usr/local/bin/
ENTRYPOINT ["ginbot-discord"]

FROM runtime-base AS matrix
COPY --from=build /out/ginbot-matrix /usr/local/bin/
ENTRYPOINT ["ginbot-matrix"]
