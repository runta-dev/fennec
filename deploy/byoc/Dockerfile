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
# user's home by Fennec on first connection; the image provides only what
# that bootstrap and everyday agent work need.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      openssh-server tini ca-certificates curl git bash procps util-linux \
      coreutils tar gzip xz-utils unzip less ripgrep jq \
 && rm -rf /var/lib/apt/lists/* /etc/ssh/ssh_host_* \
 && useradd --create-home --shell /bin/bash --uid 1000 --user-group fennec \
 && usermod --password '*' fennec
COPY deploy/byoc/sshd_config /etc/ssh/sshd_config
COPY --from=build /out/fennec-cloud /usr/local/bin/fennec-cloud
ENV LANG=C.UTF-8
VOLUME /home/fennec
EXPOSE 22
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/fennec-cloud", "init"]
