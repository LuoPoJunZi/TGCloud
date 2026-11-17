# TGCloud

TGCloud automates Telegram file transfers to cloud storage mounted in **CloudDrive2**. It combines a private Telegram Bot, persistent task queues, rule-based naming, authorized source monitoring, and Linux deployment tools.

Version: **0.0.2**. The [release](https://github.com/LuoPoJunZi/TGCloud/releases/tag/v0.0.2) provides source and Compose archives with SHA256 checksums. Build the Bot image locally on the deployment host.

## Features

- Persistent queues, task recovery, optional streaming and resource limits.
- Classification and names derived from filenames and captions.
- Administrator/member access controls and quotas.
- Explicitly authorized channel/group monitoring with source filters and transfer plans.
- WebDAV validation, HTTPS management, optional read-only external access, diagnostics and snapshots.

A successful upload and size check do not establish content-hash equality or final cloud-provider processing.

## Install

Use a native AMD64 or ARM64 Debian/Ubuntu Linux host with Docker Compose v2 and `/dev/fuse`. Host commands require Python 3.10+; Bot images use Python 3.12.

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)
```

Run in a root terminal (`sudo -i` first if needed), with curl, Python 3.10+ and CA certificates installed. The online entry downloads and verifies the stable `v0.0.2` source archive before opening the existing CloudDrive2 wizard. No Git checkout or source-path input is required. Append `--lan` for a trusted LAN; run `tgcloud` for management. A complete local source tree still supports `sudo bash install.sh` and `sudo bash install-lan.sh`.

## Docker Compose

Choose either managed script installation or standalone Compose. In a Linux root terminal with Docker Compose v2 available:

```bash
mkdir -m 700 TGCloud-compose-0.0.2 && cd TGCloud-compose-0.0.2 &&
curl -fLO https://github.com/LuoPoJunZi/TGCloud/releases/download/v0.0.2/TGCloud-v0.0.2-compose.tar.gz &&
curl -fLO https://github.com/LuoPoJunZi/TGCloud/releases/download/v0.0.2/COMPOSE-SHA256SUMS.txt &&
sha256sum -c COMPOSE-SHA256SUMS.txt &&
mkdir deployment &&
tar --no-same-owner -xzf TGCloud-v0.0.2-compose.tar.gz --strip-components=1 -C deployment &&
cd deployment && bash manage.sh
```

The new download directory must not already exist. The Bot image is built locally. Follow the [step-by-step Compose guide](docs/compose-guide.md) to check the host, initialize private configuration, configure CloudDrive2 and its dedicated WebDAV account, then start and test the Bot.

Read the [documentation index](docs/README.md), [installation guide](docs/install.md), [Compose guide](docs/compose-guide.md), [Bot guide](docs/bot-guide.md), [configuration reference](docs/configuration-guide.md), and [security notes](docs/security.md). Never commit credentials, Sessions, databases or private backups.

## License

[MIT](LICENSE). Copyright and third-party component notices are retained in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
