# Builds the plugin rootfs. The Makefile exports this image's filesystem and
# wraps it with config.json via `docker plugin create`.
#
# The build stage always runs on the host's native architecture and
# cross-compiles for the target (GOOS/GOARCH), so multi-arch builds
# (`docker buildx build --platform linux/amd64,linux/arm64 ...`) don't need
# QEMU emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY . .
RUN go mod download
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/op-sa-secret-driver . \
 && mkdir -p /out/tmp /out/run/docker/plugins

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chmod=1777 /out/tmp /tmp
COPY --from=build /out/run /run
COPY --from=build /out/op-sa-secret-driver /op-sa-secret-driver
ENTRYPOINT ["/op-sa-secret-driver"]
