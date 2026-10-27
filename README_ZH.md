# Vair

[Русский](README.md) | [English](README_EN.md) | [فارسی](README_FA.md) | **中文**

[![Download](https://img.shields.io/github/downloads/lm705/vair/total?label=Download&logo=github)](https://github.com/lm705/vair/releases)
[![Release](https://img.shields.io/github/v/release/lm705/vair?label=Release)](https://github.com/lm705/vair/releases/latest)
![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20Linux-blue)
[![License](https://img.shields.io/github/license/lm705/vair)](LICENSE)
[![Stars](https://img.shields.io/github/stars/lm705/vair?style=flat)](https://github.com/lm705/vair/stargazers)

> ⚠️ 本翻译由 AI 生成，可能不够准确，请以[俄语版本](README.md)为准。

一款 VPN 客户端，用于测试和连接 **VLESS、VMess、Trojan、Shadowsocks（包括 SS2022）、Hysteria2、TUIC、WireGuard** 协议的配置，同时也支持普通的 **SOCKS5、HTTP 与 HTTPS** 代理。基于 [Xray-core](https://github.com/XTLS/Xray-core) 和 [sing-box](https://github.com/SagerNet/sing-box) 构建。

**无需自备配置即可上手：** 程序内置了一份免费配置列表，每小时更新一次并自动检测。在 [**AUTO**](https://github.com/lm705/vair/wiki/Авто-подключение) 模式下，程序会自行测试这些配置，连接到最快的可用配置，并在其失效时自动切换到另一个。标签页的自动刷新则会让列表本身保持最新——新配置会自动加入，无需您操作。

> ℹ️ **请注意：** 该内置列表**仅在俄罗斯境内**收集和测试，因此在您所在的国家/地区可能无法使用。此时请添加您自己的订阅和配置——下述所有功能对它们同样适用。

您自己的配置同样受支持：**订阅链接**、文件和单个配置可分类到标签页中，并使用同样的测试、路由和 AUTO 功能。

**支持 Windows 与 Linux。** Windows：安装程序 `Vair_<版本>_setup.exe` 会为所有用户安装程序及一个小型辅助服务，此后 Vair 无需管理员权限即可运行，包括 TUN 模式和 kill-switch；或内置引擎的便携版 `Vair.exe`（无需安装；TUN 模式需以管理员身份运行）。Linux：`.deb`、`.rpm` 与 Arch 软件包，引擎随包安装并更新。

<a href="screenshots/main-window.png"><img src="screenshots/main-window.png" alt="Vair 主窗口：配置列表与测试结果" width="900"></a> <a href="screenshots/auto-panel.png"><img src="screenshots/auto-panel.png" alt="AUTO 面板：连接、候选列表与 IP 检查" width="450"></a>

<details>
<summary><b>更多截图</b></summary>

<br>

**AUTO 离开停止响应的服务器（示意图）**

<img src="screenshots/auto-switch.gif" alt="示意图：AUTO 发现服务器无响应后切换到下一个" width="456">

**设置：Tunnel 部分与 kill-switch 说明**

<a href="screenshots/settings.png"><img src="screenshots/settings.png" alt="设置中的 Tunnel 部分，已打开 kill-switch 说明" width="720"></a>

**Connections 面板：哪些程序通过 Vair 连接到哪里**

<a href="screenshots/connections.png"><img src="screenshots/connections.png" alt="Connections 面板：程序、目标地址与经过隧道的流量" width="900"></a>

**配置的测量历史**

<a href="screenshots/history.png"><img src="screenshots/history.png" alt="测量历史：延迟、速度与最近的检查" width="520"></a>

</details>

## 反馈

* 新闻、版本发布和测试版：Telegram 频道 [**t.me/vair705**](https://t.me/vair705)。*(俄语)*
* 如有建议、错误或任何反馈，请在 [**Issues**](https://github.com/lm705/vair/issues) 中提交。可以使用您方便的任何语言撰写。
* 如需直接联系：Telegram [**t.me/mial705**](https://t.me/mial705)。

## 快速开始

1. 从 [**Releases**](https://github.com/lm705/vair/releases) 页面下载：`Vair_<版本>_setup.exe`（安装程序）或 `Vair.exe`（便携版，无需安装即可运行）。

   在 Linux 上安装下载的软件包：

   ```bash
   sudo apt install ./vair_*_amd64.deb             # Debian, Ubuntu, Kali
   sudo dnf install ./vair-*.x86_64.rpm            # Fedora
   sudo pacman -U ./vair-*-x86_64.pkg.tar.zst      # Arch
   ```

   Windows 版文件没有数字签名，因此首次运行时 Windows 可能会显示“Windows protected your PC”：点击“More info”→“Run anyway”。

2. 打开 **SOURCES** 标签页并点击 **RELOAD**，即可获取最新的配置列表。
3. 点击 **PING ALL** 或 **SPEED ALL** 进行检测，然后连接任意可用配置——或者开启 **AUTO**，让程序自动完成。

更多内容见 Wiki 中的[入门指南](https://github.com/lm705/vair/wiki/Начало-работы)。

## 系统要求

* **Windows 10 或 11**，64 位，需要 WebView2 Runtime：Windows 11 已内置；若缺失，安装程序会自动安装。
* **Linux x86_64**，需要 GTK 4.10+ 和 WebKitGTK 6.0：Ubuntu 24.04+、Debian 13+、较新的 Fedora 和 Arch、Kali。Ubuntu 22.04 不支持。
* xray-core 和 sing-box 引擎随程序一起提供。

## 功能

* **协议** — VLESS / VMess / Trojan / Shadowsocks / SS2022 / Hysteria2 / TUIC / WireGuard，以及 SOCKS5、HTTP 与 HTTPS 代理，支持 TLS 与 Reality。
* **两种模式** — 系统代理（HTTP/SOCKS）和 TUN：全部流量走隧道。
* [**自动连接（AUTO）**](https://github.com/lm705/vair/wiki/Авто-подключение) — 自动连接到最快的可用配置，并在其不再应答时自动切换。
* **测试** — 对每个配置测量延迟和速度，支持批量；结果在重启后保留并汇成历史。另有检测：哪台服务器能打开哪些服务。
* **路由** — 五种模式：全部流量走 VPN；除所选国家（🇷🇺 🇨🇳 🇮🇷 🇰🇿）网站外的全部流量；仅俄罗斯被封锁的资源；仅中国被封锁的资源；仅所选网站。另可为域名、IP 和应用设置自定义规则。
* **防泄漏** — TUN 模式下的 kill-switch 与 DNS 泄漏防护，Windows 与 Linux 均可使用；即使关闭程序仍然保持的阻断（Windows）；每次连接后的泄漏检查（Windows）；可选择如何处理 IPv6。
* **看得到隧道里在跑什么** — 连接面板：程序、目标、生效的规则与流量，并用单独的表格显示绕过 VPN 的连接。
* **链式代理与绕过 DPI** — 依次经过多个节点；TLS 握手分片和浏览器 TLS 指纹（uTLS）。
* **标签页与订阅源** — 订阅链接、文件与私有 GitHub 仓库，支持自动刷新与过滤；多达约 250,000 条配置依然流畅（SQLite）。
* **在手机上使用 VPN** — 从 TUN 模式将代理共享到局域网，以及[用手机控制程序](https://github.com/lm705/pc-vpn-to-phone)。
* **实用功能** — 二维码、手动创建配置（包括 Xray 与 sing-box 原始 JSON）、自动更新、开机自启、深色与浅色主题、俄语和英语界面。

## 文档

* [**Wiki**](https://github.com/lm705/vair/wiki) — 完整手册：设置、模式、路由、链式代理、测试、常见问题。*(俄语)*
* 📱 [**如何在手机上使用此电脑的 VPN**](https://github.com/lm705/pc-vpn-to-phone) — 通过 Wi-Fi 共享，以及借助 Tailscale 随时随地访问。

## vless_alive.txt 和 all_alive.txt

一份可在俄罗斯境内正常使用的免费 VLESS 配置列表。

已预加载到程序的 **SOURCES** 标签页中。点击 **RELOAD** 按钮可获取该文件的最新版本。

`all_alive.txt` 的区别在于，它包含所有协议的配置：VLESS、VMess、Trojan、Shadowsocks、Hysteria2 和 TUIC。

配置每小时更新一次，并在位于俄罗斯封锁最严重地区之一的服务器上自动检测可用性和速度。不可用和速度慢的配置会被过滤掉。大多数配置在更新时都是可用的。

> ⚠️ **自 2026 年 10 月 19 日起，该列表不再每小时更新。** 我已迁居（也建议您如此——时间比想象的要少），因此无法再维持定期更新。此后将由另一位志愿者利用空闲时间更新，没有固定周期。可用性与速度检测仍会保留，但配置可能不如以往新鲜——建议同时添加您自己的订阅源。

配置来源于互联网上的公开资源。配置的收集与自动检测由 [vair-collector](https://github.com/lm705/vair-collector) 脚本完成。

名称中的国家可能与实际出口 IP 不一致。

## 免责声明

本软件按「原样」(*as is*) 提供，不作任何明示或暗示的保证。

本程序是用于处理网络协议的技术工具。**这不是广告、推广，也不提供 VPN 服务。** 本程序不是 VPN 提供商，不包含自有服务器，也不提供访问被封锁资源的服务。

`vless_alive.txt` 和 `all_alive.txt` 文件包含从互联网公开来源收集的配置。作者不拥有、不运营、也不控制配置中所列出的服务器。使用这些服务器时的可用性、安全性和隐私性均不作保证。

用户须自行承担在其所属司法辖区内使用本软件时遵守法律的全部责任。

>## 支持
>
>[DALINK](https://dalink.to/lm705)（不使用 VPN 打开可显示卢布价格）
>
>相关"支付"地址信息仅供参考。
>
>ERC20:
>
>```
>0x37072eb42019a258a801df002e5c8d5542709165
>```
>
>TRC20:
>
>```
>TGz3wkGFL7PNjSoxpDnM5WgXEvod3qfz3k
>```
>
>SOL:
>
>```
>F7pVS8N74ARDJYmiTqFeViRVHZhXJ8toJsxyrmWstfmi
>```
>
>TON:
>
>```
>UQBxGaG0hBVKarg6TJVaqvCN0hOFAodQjnCZ9rdnUaWxcbDV
>```

## 仓库统计

<a href="https://www.star-history.com/?repos=lm705%2Fvair&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=lm705/vair&type=date&theme=dark&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=lm705/vair&type=date&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=lm705/vair&type=date&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
 </picture>
</a>
