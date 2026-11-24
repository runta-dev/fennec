# Fennec self-hosted cloud computer (BYOC)

Run Fennec's cloud computer on your own server. No Runta account or Runta
service is involved: your Mac connects to the server directly over SSH, pins
its host key when you pair, and verifies its machine identity on every
connection.

The menu-bar app does not show self-hosted computers yet; pair with `fen`.

## Deploy

On your Mac, print this Mac's device key:

```sh
fen cloud byoc key
```

On the server (any Linux host with Docker, amd64 or arm64):

```sh
docker run -d --name fennec-cloud --restart unless-stopped \
  -p 2222:22 -v fennec-home:/home/fennec \
  -e FENNEC_AUTHORIZED_KEY='<output of fen cloud byoc key>' \
  ghcr.io/runta-dev/fennec-cloud:1
```

Images are published to GHCR for `byoc-v*` release tags. Until the first
release, build the image yourself (see [Build and verify](#build-and-verify)) and
use that tag instead.

`docker logs fennec-cloud` prints the machine ID and the host key fingerprint.
Pair from your Mac with that fingerprint:

```sh
fen cloud byoc pair <server>:2222 --fingerprint SHA256:...
```

Read the fingerprint over a channel you trust (your own SSH session to the
server). Pairing refuses any other host key.

## What the image contains

- One user, `fennec`, reachable only with authorized device keys. There is no
  password login and no root login. `fennec` has passwordless `sudo`, so agents
  can install packages: the container, not the user account, is the isolation
  boundary. Keep that in mind when deciding what to mount into it.
- The `/home/fennec` volume holds everything that must survive the container:
  agents, sessions, credentials synchronized by Fennec, and a root-owned
  `.fennec-host` directory with the SSH host key, the machine identity and the
  authorized device list. Replacing the container keeps the same paired
  computer; a new volume is a different computer and must be paired again.
- A development base: git and git-lfs, build-essential, pkg-config, OpenSSL
  and zlib headers, Python 3 with pip and venv, ripgrep, fd, jq, curl, wget,
  rsync, tmux and common network and archive tools. On first connection Fennec
  installs Node.js, Herdr, its terminal service and the agents into the user's
  home, and keeps the terminal service matched to the Mac's version.
- Debian's Python is externally managed: use `python3 -m venv`, or `sudo apt
  install python3-<package>`, rather than a system-wide `pip install`.

## Manage devices

```sh
docker exec fennec-cloud fennec-cloud devices
docker exec fennec-cloud fennec-cloud authorize '<device key>'
docker exec fennec-cloud fennec-cloud revoke SHA256:...
docker exec fennec-cloud fennec-cloud fingerprint
```

On the Mac, `fen cloud byoc status` shows the pairing and `fen cloud byoc
unpair <machine-id>` removes it and returns to your Runta account, if any.

## Notes

- Restarting the container ends running agent processes, as rebooting a
  machine would. Their files and conversations remain on the volume.
- Expose the SSH port only as widely as you need, for example over a private
  network or VPN. Access requires an authorized key either way.
- Do not mount the Docker socket into the container: it would give agents
  control of the host.

## Build and verify

```sh
docker build -f deploy/byoc/Dockerfile -t fennec-cloud:local .
FENNEC_BYOC_IMAGE=fennec-cloud:local go test ./deploy/byoc
FENNEC_BYOC_IMAGE=fennec-cloud:local FENNEC_BYOC_BOOTSTRAP=1 go test ./deploy/byoc
```

The second test pairs and connects with Fennec's client code; the third also
runs the first-connection bootstrap. `.github/workflows/byoc-image.yml` runs
both on every change and publishes multi-arch, signed images for `byoc-v*`
tags.
