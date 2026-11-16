# TGCloud

Self-hosted Telegram-to-cloud transfers through CloudDrive2. The current version is **0.0.1**, a local source edition that has not been published.

From a complete checkout on a supported Linux host, run `sudo bash install.sh` for public HTTPS or `sudo bash install-lan.sh` for a trusted LAN. Both open the CloudDrive2 manager directly. The installed command is `tgcloud`.

Build the single Compose bundle with `python3 -B scripts/build_compose.py --output /absolute/new-output`. See the [installation guide](docs/install.md), [Compose guide](docs/compose-guide.md), [Bot guide](docs/bot-guide.md) and [CloudDrive2 configuration](docs/configuration-guide.md).

Persistent queues, deduplication, restart recovery, authorized listening, streaming backpressure and disk protection remain part of the shared core. WebDAV acceptance and size checks do not establish a content hash or the cloud provider's final ingestion.

The intended repository is [LuoPoJunZi/TGCloud](https://github.com/LuoPoJunZi/TGCloud). No online installer, release or image is claimed to be available. Existing installations from another project are not automatically adopted or migrated.

Prior project records are preserved in the [history archive](archive/README.md). The local regression checks now pass; platform-dependent integration checks still require their documented environment. See the [development guide](docs/development.md) for the validation scope. Historical acceptance does not establish TGCloud production readiness.

TGCloud derives from the CloudDrive2 implementation of [TG2Cloud](https://github.com/LuoPoJunZi/TG2Cloud), originally based on [whyhhh20/TG115](https://github.com/whyhhh20/TG115). Original copyrights, upstream acknowledgement and the [MIT license](LICENSE) are preserved.

[中文](README.md) · [Documentation](docs/README.md)
