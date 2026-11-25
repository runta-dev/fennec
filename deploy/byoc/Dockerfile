# syntax=docker/dockerfile:1
# Fennec self-hosted cloud computer. Build from the repository root:
#   docker build -f deploy/byoc/Dockerfile -t fennec-cloud .
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/fennec-cloud ./cmd/fennec-cloud
COPY internal/cloudimage ./internal/cloudimage
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags='-s -w' -o /out/fennec-cloud ./cmd/fennec-cloud

# Node.js for agents, the version and checksums pinned by Fennec's preparation
# (internal/setup NodeSetupScript; deploy/byoc/image_test.go keeps them equal).
# fennec-cloud init places it in the user's home on a volume's first start, so
# the first connection does not depend on nodejs.org.
FROM debian:bookworm-slim AS node
ARG TARGETARCH
ARG NODE_VERSION=22.22.0
ARG NODE_SHA256_AMD64=c33c39ed9c80deddde77c960d00119918b9e352426fd604ba41638d6526a4744
ARG NODE_SHA256_ARM64=25ba95dfb96871fa2ef977f11f95ea90818c8fa15c0f2110771db08d4ba423be
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
 && case "$TARGETARCH" in \
      amd64) arch=x64; sum=$NODE_SHA256_AMD64 ;; \
      arm64) arch=arm64; sum=$NODE_SHA256_ARM64 ;; \
      *) echo "unsupported architecture $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && curl --fail --silent --show-error --location --retry 3 \
      "https://nodejs.org/dist/v$NODE_VERSION/node-v$NODE_VERSION-linux-$arch.tar.gz" -o /node.tar.gz \
 && printf '%s  %s\n' "$sum" /node.tar.gz | sha256sum -c - \
 && mkdir -p /seed \
 && tar -xzf /node.tar.gz -C /seed \
 && mv "/seed/node-v$NODE_VERSION-linux-$arch" /seed/node \
 && test "$(/seed/node/bin/node --version)" = "v$NODE_VERSION"

FROM debian:bookworm-slim
LABEL org.opencontainers.image.title="Fennec cloud computer" \
      org.opencontainers.image.description="Self-hosted cloud computer for Fennec coding agents" \
      org.opencontainers.image.source="https://github.com/runta-dev/fennec"
# The terminal service, agents and Herdr are installed into the user's home by
# Fennec on first connection; Node.js is seeded from the image. The image provides the bootstrap
# tools and a general development environment for agents; agents can install
# more with sudo, since the container is the isolation boundary.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      openssh-server tini ca-certificates sudo bash procps util-linux coreutils \
      tar gzip bzip2 xz-utils zstd unzip zip \
      curl wget openssh-client rsync iproute2 iputils-ping dnsutils netcat-openbsd \
      git git-lfs build-essential pkg-config libssl-dev zlib1g-dev \
      python3 python3-pip python3-venv python3-dev \
      ripgrep fd-find jq less tree file lsof htop tmux vim-tiny nano \
 && rm -rf /var/lib/apt/lists/* /etc/ssh/ssh_host_* \
 && ln -s /usr/bin/fdfind /usr/local/bin/fd \
 && useradd --create-home --shell /bin/bash --uid 1000 --user-group fennec \
 && usermod --password '*' fennec \
 && echo 'fennec ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/fennec \
 && chmod 0440 /etc/sudoers.d/fennec \
 && visudo --check --quiet
COPY deploy/byoc/sshd_config /etc/ssh/sshd_config
COPY --from=build /out/fennec-cloud /usr/local/bin/fennec-cloud
COPY --from=node /seed/node /opt/fennec/seed/node
ENV LANG=C.UTF-8
VOLUME /home/fennec
EXPOSE 22
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/fennec-cloud", "init"]
