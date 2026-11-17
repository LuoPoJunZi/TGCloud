# TGCloud

TGCloud automates Telegram file transfers to cloud storage mounted in **CloudDrive2**. It combines a private Telegram Bot, persistent task queues, rule-based naming, authorized source monitoring, and Linux deployment tools.

Version: **0.0.1**. The [release](https://github.com/LuoPoJunZi/TGCloud/releases/tag/v0.0.1) provides source and Compose archives with SHA256 checksums. Build the Bot image locally on the deployment host.

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
git clone --depth 1 --branch v0.0.1 https://github.com/LuoPoJunZi/TGCloud.git TGCloud && cd TGCloud && sudo bash install.sh
```

Git, Python 3.10+ and root/sudo are required. The `TGCloud` destination must not already exist. The wizard detects its complete source tree and configures CloudDrive2 only. Run `sudo tgcloud` for management; use `sudo bash install-lan.sh` for a trusted LAN.

## Docker Compose

Choose either managed script installation or standalone Compose. For standalone Compose:

```bash
git clone --depth 1 --branch v0.0.1 https://github.com/LuoPoJunZi/TGCloud.git TGCloud
cd TGCloud
python3 -B scripts/build_compose.py --output "$PWD/build/compose"
cd build/compose/clouddrive2
sudo bash manage.sh
```

The output directory must not already exist. Alternatively, download `TGCloud-v0.0.1-compose.tar.gz` and verify `COMPOSE-SHA256SUMS.txt`, as shown in the [Compose guide](docs/compose-guide.md). Initialize private configuration, configure the gateway and dedicated WebDAV account, then start the Bot.

Read the [documentation index](docs/README.md), [installation guide](docs/install.md), [Compose guide](docs/compose-guide.md), [Bot guide](docs/bot-guide.md), [configuration reference](docs/configuration-guide.md), and [security notes](docs/security.md). Never commit credentials, Sessions, databases or private backups.

## License

[MIT](LICENSE). Copyright and third-party component notices are retained in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
