> 历史档案：以下为原项目记录，不是 TGCloud 0.0.1 的当前教程或验收结论。[归档说明](../README.md)。

<p align="center">
  <img src="assets/brand/tg2cloud-logo.svg" alt="TG2Cloud — From Telegram to Your Cloud" width="520">
</p>

<p align="center">
  <strong>Telegram file transfer · CloudDrive2 / OpenList · Linux one-line deployment</strong>
</p>

<p align="center">
  <a href="https://github.com/LuoPoJunZi/TG2Cloud/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/LuoPoJunZi/TG2Cloud?style=flat-square&amp;label=release&amp;color=1677ff"></a>
  <a href="https://github.com/LuoPoJunZi/TG2Cloud/actions/workflows/tests.yml"><img alt="Tests" src="https://img.shields.io/github/actions/workflow/status/LuoPoJunZi/TG2Cloud/tests.yml?branch=main&amp;style=flat-square&amp;label=tests"></a>
  <a href="LICENSE"><img alt="MIT License" src="https://img.shields.io/github/license/LuoPoJunZi/TG2Cloud?style=flat-square&amp;color=22a06b"></a>
</p>

<p align="center">
  <a href="docs/VPS-INSTALL.md"><strong>Installation</strong></a> ·
  <a href="docs/README.md">Repository docs</a> ·
  <a href="https://github.com/LuoPoJunZi/TG2Cloud/releases">Releases</a> ·
  <a href="https://tg2cloud.pages.dev/">Documentation website</a> ·
  <a href="README.md">简体中文</a>
</p>

TG2Cloud is a self-hosted personal file transfer tool. Send or forward files to your own Telegram Bot; your Linux host queues, downloads and writes them to cloud storage mounted by you.

Telegram → TG2Cloud → rclone → CloudDrive2 / OpenList WebDAV → your cloud storage

Optional [administrator monitoring and transfer plans](manuals/listening.md) are disabled by default, with per-source filters, naming, directories, approvals and recovery.

## Features

- Two editions share one Bot core while keeping separate configuration, databases and Sessions.
- Persistent queues, deduplication, confirmed cancellation recovery, retained failed files and caption-based naming.
- Regular downloads and large-file streaming with adaptive concurrency and disk protection.
- Public HTTPS / trusted LAN deployment, storage recommendations and configuration-preserving upgrades.

Current stable version: **[v1.5.9](https://github.com/LuoPoJunZi/TG2Cloud/releases/tag/v1.5.9)**, for Linux. See the [release and compatibility record](releases/release-1.5.9.md).

v1.5.9 adds download stall protection and queue diagnostics; see the [user-confirmed CloudDrive2 script upgrade and transfer record](releases/release-1.5.9.md).

## Getting started

1. Prepare your Telegram Bot, API information and server using the [information checklist](manuals/setup-checklist.md). Never put real credentials in public documents.
2. Choose CloudDrive2 or OpenList and either the public VPS or trusted LAN entry. Review the wizard's installation plan before confirming.
3. Mount cloud storage in your own gateway, then configure and verify WebDAV as described in the installation guide. You manage cloud-account authorization yourself.
4. Send or forward files to the Bot and check its receipt. See [task usage and recovery](manuals/recovery.md) for commands, failed tasks and verification levels.

Both editions share transfer capabilities but have different gateway configuration and independent instance data. Upgrade an existing instance through its matching entry, preserving configuration; do not uninstall first.

## Quick start

The public/LAN entries offer setup for a missing Edition or its maintenance menu for an existing instance. After confirming manager installation, run `tg2cloud` locally. Uninstall stays Edition-scoped; the v1.4.7 source retires the standalone Shell entries and retains the unified menu and CLI preview. Menu, update and cleanup boundaries are in the [operations guide](manuals/operations.md#本地管理菜单).

v1.4.7 provides unified management. External WebDAV provides read-only mobile access. The [unified entry](manuals/external-webdav.md#统一管理), `tg2cloud` → **15. 外部 WebDAV**, handles installation and maintenance in its submenu without a separate installation command. Update existing local managers; use option 8 in the WebDAV submenu to upgrade that add-on.

Requires Debian / Ubuntu with native 64-bit userspace, root, Python 3.10+, curl and an interactive terminal, with access to GitHub, Telegram, container registries and the destination. CloudDrive2 also requires `/dev/fuse`.

Linux system requirements, public NAT prerequisites and platform limits are covered in the [installation guide](manuals/vps-install.md) and [architecture guide](manuals/architectures.md).

### Public VPS

Prepare a domain pointing to the VPS and allow inbound TCP 80/443. Select CloudDrive2 or OpenList in the wizard:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TG2Cloud/main/install.sh)
```

The wizard collects information, checks the environment and asks for confirmation. Business code comes only from official stable Releases. For an existing instance, first append `--edition clouddrive2 --check` (use `openlist` for OpenList). This check does not upgrade. Then rerun the entry without arguments, choose the Edition and installation/upgrade, and confirm to also install the offline manager. Explicit flags remain compatible. See the [VPS guide](manuals/vps-install.md).

### Trusted LAN / NAS

Without a public domain, use the separate LAN entry and enter the host's private IPv4. The Linux requirements above still apply; not every NAS OS is supported:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TG2Cloud/main/install-lan.sh)
```

LAN HTTP is unencrypted: do not expose it through public port forwarding or use it to bypass public HTTPS. See the [LAN guide](manuals/lan-install.md).

### Uninstall preview

```bash
tg2cloud uninstall --edition all --check
```

The `tg2cloud` menu also provides removal for the selected Edition. This command only previews; it does not uninstall. Actual complete removal deletes the selected instance's data and recognized backups. To preserve data, use `--keep-data`; save independent backups outside the removal paths first. See the [uninstall guide](manuals/uninstall.md).

## Documentation

Browse repository guides by topic; see the [full index](manuals/readme.md) and [release notes](release-notes.md).

| Topic | Repository documents |
| --- | --- |
| Installation and configuration | [Public VPS installation / upgrade](manuals/vps-install.md) · [LAN / NAS installation](manuals/lan-install.md) · [Information checklist](manuals/setup-checklist.md) |
| Docker Compose | [Step-by-step deployment, setup and backups](manuals/compose.md) · [Optional HTTPS wizard](manuals/compose.md#可选-https-配置向导) |
| Gateway and first transfer | [CloudDrive2 / OpenList configuration and verification](manuals/storage.md) · [Bot usage, menus and commands](manuals/bot-usage.md) |
| Naming and common questions | [Video, caption and album naming](manuals/naming.md) · [FAQ](manuals/faq.md) |
| Configuration and backups | [Configuration fields and runtime paths](manuals/configuration.md) · [Backup scope and restore preflight](manuals/backup-restore.md) |
| Family and mobile reading | [Bot family grants](manuals/family-sharing.md) · [Read-only external WebDAV](manuals/external-webdav.md) |
| Optional features | [Classification, notifications and quotas](manuals/feature-enhancements.md) · [Existing Nginx reuse](manuals/docker-nginx.md) |
| Operation and troubleshooting | [Management commands, logs and HTTPS maintenance](manuals/operations.md) · [Task recovery, verification and backup](manuals/recovery.md) |
| Current maintenance | [v1.5.9 release and compatibility](releases/release-1.5.9.md) |
| Uninstall | [Uninstall guide](manuals/uninstall.md) |
| Development and tests | [Contributing](policies/contributing.md) · [Repository structure](manuals/development/repository.md) · [Linux development and tests](manuals/development/development.md) |
| Versions and releases | [Complete version history](changelog.md) · [Current release notes](release-notes.md) |
| Branding and components | [Brand guide](manuals/development/brand-guide.md) · [Third-party components](manuals/third-party-components.md) |

## Boundaries

- Transfer only content you are authorized to save. Monitoring requires explicit administrator authorization; content protection is respected. 115 is an example, not the only destination.
- Cloud authentication stays in your own CloudDrive2 / OpenList instance. TG2Cloud has no telemetry or credential callback.
- WebDAV size verification is not a content hash or an official cloud-provider completion result. Verify important files independently.
- Never publish tokens, passwords, Sessions, databases, configuration or backups. Back up before upgrading; do not overwrite an instance with an unknown identity. See [Security](policies/security.md).

## Acknowledgements and Project Origin

<table>
  <tr>
    <td align="center" width="260" valign="top" nowrap>
      <a href="https://github.com/LuoPoJunZi">
        <img src="https://github.com/LuoPoJunZi.png?size=96" width="72" height="72" alt="LuoPoJunZi"><br>
        <strong>LuoPoJunZi</strong>
      </a><br>
      <sub>TG2Cloud maintainer</sub>
    </td>
    <td align="center" width="260" valign="top" nowrap>
      <a href="https://github.com/whyhhh20">
        <img src="https://github.com/whyhhh20.png?size=96" width="72" height="72" alt="whyhhh20"><br>
        <strong>whyhhh20</strong>
      </a><br>
      <sub>Original author of upstream TG115</sub>
    </td>
  </tr>
</table>

Thanks to **whyhhh20** for creating and publishing the upstream [TG115 project](https://github.com/whyhhh20/TG115).

TG2Cloud evolved from that project, retains the MIT license, original copyright, and required attribution, and continues to improve the reliability, deployment safety, and operational experience of the Telegram → rclone → CloudDrive2/OpenList → user-owned-cloud path.

Thanks also to Telethon, rclone, Docker, and the other open-source projects involved, and to CloudDrive2 for its WebDAV and cloud-mount features. Each third-party component remains subject to its own license and terms.

## License and Disclaimer

This project is licensed under the [MIT License](../../LICENSE).

TG2Cloud is not affiliated with, authorized by, or officially partnered with Telegram, CloudDrive2, OpenList, any cloud-storage provider, or their operators. Users are responsible for evaluating account, data, network, VPS privilege, and third-party closed-source component risks.
