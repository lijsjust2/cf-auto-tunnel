FROM golang:1.22-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG CLOUDFLARED_VERSION

RUN apk add --no-cache upx tzdata curl jq

WORKDIR /src

COPY go.mod ./
COPY *.go ./
COPY static/ ./static/

# 编译主程序
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o cf-tunnel-manager . \
    && upx --best --lzma cf-tunnel-manager

# 下载 cloudflared
RUN if [ -z "${CLOUDFLARED_VERSION}" ]; then \
        echo "CLOUDFLARED_VERSION not specified, fetching latest from GitHub..." && \
        CLOUDFLARED_VERSION=$(curl -sL https://api.github.com/repos/cloudflare/cloudflared/releases/latest | jq -r '.tag_name'); \
    fi && \
    if [ -z "${CLOUDFLARED_VERSION}" ] || [ "${CLOUDFLARED_VERSION}" = "null" ]; then \
        echo "ERROR: Unable to determine cloudflared version. Please set CLOUDFLARED_VERSION build arg."; \
        exit 1; \
    fi && \
    echo "Using cloudflared version: ${CLOUDFLARED_VERSION}" && \
    CLOUDFLARED_VERSION_CLEAN=$(echo "${CLOUDFLARED_VERSION}" | sed 's/^v//') && \
    CLOUDFLARED_URL="https://github.com/cloudflare/cloudflared/releases/download/${CLOUDFLARED_VERSION}/cloudflared-linux-${TARGETARCH}" && \
    echo "Downloading cloudflared from: ${CLOUDFLARED_URL}" && \
    curl -fSL "${CLOUDFLARED_URL}" -o /src/cloudflared && \
    chmod +x /src/cloudflared && \
    echo "cloudflared ${CLOUDFLARED_VERSION} downloaded successfully"

# 最终镜像
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /src/cf-tunnel-manager /app/cf-tunnel-manager
COPY --from=builder /src/cloudflared /app/cloudflared

WORKDIR /app

VOLUME /app/data

ENV WEB_PORT=8080
ENV DATA_DIR=/app/data
ENV CLOUDFLARED_PATH=/app/cloudflared

EXPOSE 8080

ENTRYPOINT ["/app/cf-tunnel-manager"]
