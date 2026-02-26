ARG BUILD_FROM=ghcr.io/home-assistant/amd64-base:3.21
ARG BUILD_ARCH=amd64
FROM golang:1.24-alpine AS builder

ARG BUILD_ARCH

RUN apk add --no-cache git

WORKDIR /src
COPY src/go.mod src/go.sum ./
RUN go mod download

COPY src/ .
RUN \
    if [ "${BUILD_ARCH}" = "aarch64" ]; then \
        export GOARCH=arm64; \
    elif [ "${BUILD_ARCH}" = "amd64" ]; then \
        export GOARCH=amd64; \
    fi \
    && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /nanitd ./cmd/nanitd

FROM ${BUILD_FROM}

RUN apk add --no-cache ca-certificates tzdata ffmpeg

COPY --from=builder /nanitd /usr/bin/nanitd
COPY run.sh /

RUN chmod a+x /run.sh
RUN mkdir -p /data

CMD [ "/run.sh" ]
