# TGCloud

**Telegram file automation · CloudDrive2 · Linux deployment**

[Latest release](https://github.com/LuoPoJunZi/TGCloud/releases/latest) · [Documentation](docs/README.md) · [中文](README.md)

TGCloud transfers files sent or forwarded to your Telegram Bot into cloud storage mounted in CloudDrive2. It provides persistent queues, recovery, classification and naming, streaming, administrator/member controls, authorized source monitoring, WebDAV validation, diagnostics and backups.

Current stable release: **[v0.0.7](https://github.com/LuoPoJunZi/TGCloud/releases/tag/v0.0.7)**. GitHub Actions validates and builds the source archive (including native Linux AMD64/ARM64 `tgcloud-host` host-management binaries), Compose archive, and checksum files. The Bot image is built locally on the deployment host.

## Quick install

Use native AMD64 or ARM64 Debian/Ubuntu Linux with `/dev/fuse`, an interactive root terminal, curl, Python 3.10+ and CA certificates. Bot images use Python 3.12. Prepare your Telegram API ID, API Hash, Bot Token and administrator ID.

For a public VPS with a domain pointing to it and TCP ports 80/443 available:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)
```

Run `sudo -i` first if needed. The entry downloads and verifies the stable `v0.0.7` source archive before opening the wizard. No Git checkout, source-path input or edition selection is required. Resource recommendations use measured host resources. The default instance directory is `/opt/tgcloud`; run `tgcloud` for management.

For a trusted LAN:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh) --lan
```

Keep HTTP within the host or a trusted LAN. A complete local source tree still supports `sudo bash install.sh` and `sudo bash install.sh --lan`.

## Docker Compose

Choose either managed script installation or standalone Compose. Prepare Docker Engine, Compose v2, FUSE, Python 3.10+, curl, tar and sha256sum. In a Linux root terminal:

```bash
mkdir -m 700 /root/TGCloud-Compose && \
cd /root/TGCloud-Compose && \
curl -fLO https://github.com/LuoPoJunZi/TGCloud/releases/download/v0.0.7/TGCloud-v0.0.7-compose.tar.gz && \
curl -fLO https://github.com/LuoPoJunZi/TGCloud/releases/download/v0.0.7/COMPOSE-SHA256SUMS.txt && \
sha256sum -c COMPOSE-SHA256SUMS.txt && \
tar --no-same-owner -xzf TGCloud-v0.0.7-compose.tar.gz --strip-components=1
```

The `/root/TGCloud-Compose` directory must not already exist. These commands download the `v0.0.7` deployment archive and its SHA256 checksum file. Deployment files are extracted directly into `/root/TGCloud-Compose`.

After extraction, run `bash manage.sh` from the same directory to open the setup menu.

Initialize configuration, start the gateway, mount cloud storage and create the dedicated WebDAV account in CloudDrive2, then start and test the Bot. Disk defaults are calculated from the deployment volume; setup does not inspect host CPU, RAM or the Docker data filesystem. For later management:

```bash
cd /root/TGCloud-Compose
bash manage.sh
```

## First transfer and documentation

Run the readiness and WebDAV checks, send a small file to the Bot, inspect `/queue` or `/tasks`, and check it in the cloud provider's official client. An upload and size check do not establish content-hash equality or final provider processing.

Read the [installation guide](docs/install.md), [Compose guide](docs/compose-guide.md), [Bot guide](docs/bot-guide.md), [naming guide](docs/naming-guide.md), [monitoring guide](docs/listening-guide.md), [configuration reference](docs/configuration-guide.md), [operations guide](docs/operations-guide.md), [backup guide](docs/backup-and-recovery.md), [FAQ](docs/faq.md), and [development guide](docs/development.md). See the [documentation index](docs/README.md) for all topics.

Never commit credentials, Sessions, databases or private backups. Base64 is encoding, not encryption. See the [security notes](docs/security.md).

## Acknowledgements and license

Thanks to [whyhhh20/TG115](https://github.com/whyhhh20/TG115) for its open-source contribution. The original `TG115 Contributors` copyright remains in [LICENSE](LICENSE), under the MIT license.

Dependencies and bundled components retain their own licenses. Thanks to Telethon, rclone, Docker, Nginx and Certbot. CloudDrive2 is governed by its official terms. TGCloud has no official affiliation with Telegram, CloudDrive2 or cloud-storage providers.
