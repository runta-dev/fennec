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

FROM debian:bookworm-slim
LABEL org.opencontainers.image.title="Fennec cloud computer" \
      org.opencontainers.image.description="Self-hosted cloud computer for Fennec coding agents" \
      org.opencontainers.image.source="https://github.com/runta-dev/fennec"
# The terminal service, agents, Node.js and Herdr are installed into the
# user's home by Fennec on first connection. The image provides the bootstrap
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
ENV LANG=C.UTF-8
VOLUME /home/fennec
EXPOSE 22
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/fennec-cloud", "init"]
