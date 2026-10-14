# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

FROM golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
RUN apk add --no-cache build-base ca-certificates git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 CGO_CFLAGS="-Wno-error=missing-braces" GOOS=linux \
    go build -tags sqlite_fts5 -trimpath -ldflags="-s -w" -o /out/whatscli ./cmd/whatscli

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
LABEL org.opencontainers.image.title="WhatsCLI" \
    org.opencontainers.image.description="WhatsApp for Agents" \
    org.opencontainers.image.source="https://github.com/gabrielMalonso/whatscli" \
    org.opencontainers.image.licenses="MIT"
RUN apk add --no-cache ca-certificates ffmpeg tzdata \
    && adduser -D -u 10001 -h /home/wacli wacli \
    && mkdir -p /data/store /data/state /data/config /data/cache \
    && chown -R wacli:wacli /data
ENV HOME=/home/wacli \
    WACLI_STORE_DIR=/data/store \
    XDG_STATE_HOME=/data/state \
    XDG_CONFIG_HOME=/data/config \
    XDG_CACHE_HOME=/data/cache
VOLUME ["/data"]
WORKDIR /data
COPY --from=build /out/whatscli /usr/local/bin/whatscli
COPY LICENSE README.md /usr/share/doc/whatscli/
RUN ln -s whatscli /usr/local/bin/wacli
USER wacli
ENTRYPOINT ["whatscli"]
CMD ["--help"]
