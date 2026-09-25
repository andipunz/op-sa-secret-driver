# Builds the plugin rootfs. The Makefile exports this image's filesystem and
# wraps it with config.json via `docker plugin create`.
FROM golang:1.24-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY . .
RUN go mod download
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/op-sa-secret-driver . \
 && mkdir -p /out/tmp /out/run/docker/plugins

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chmod=1777 /out/tmp /tmp
COPY --from=build /out/run /run
COPY --from=build /out/op-sa-secret-driver /op-sa-secret-driver
ENTRYPOINT ["/op-sa-secret-driver"]
