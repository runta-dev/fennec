# Fennec self-hosted cloud computer (BYOC)

Run Fennec's cloud computer on your own server. No Runta account or Runta
service is involved: your Mac connects to the server directly over SSH, pins
its host key when you pair, and verifies its machine identity on every
connection.

Pair with `fen`. Once paired, the menu bar shows the server under Account and
unpairs it from the account menu.

## Deploy with one command

If you can already `ssh` to the server and it has Docker:

```sh
fen cloud byoc deploy you@your-server
```

This uses your own SSH configuration to start the image on the server (port
2222 by default), reads the computer's host key back over that SSH connection,
and pairs with it. Options: `--port`, `--image`, `--name`, `--volume`, and
`--address <host>[:<port>]` when the Mac reaches the server at a different
address than SSH does (for example through a VPN instead of a jump host).
Docker runs as your SSH user, or through `sudo` when that needs no password.

## Deploy manually

On your Mac, print this Mac's device key:

```sh
fen cloud byoc key
```

On the server (any Linux host with Docker, amd64 or arm64):

```sh
docker run -d --name fennec-cloud --restart unless-stopped \
  -p 2222:22 -v fennec-home:/home/fennec \
  -e FENNEC_AUTHORIZED_KEY='<output of fen cloud byoc key>' \
  docker.io/shiqimei/fennec-cloud:1
```

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
  rsync, tmux and common network and archive tools. Node.js 22 is built in and
  copied into the home on a volume's first start; on first connection Fennec
  installs Herdr, its terminal service and the agents there, and keeps the
  terminal service matched to the Mac's version.
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
both on every change and publishes multi-arch, signed images to Docker Hub for `byoc-v*`
tags.
