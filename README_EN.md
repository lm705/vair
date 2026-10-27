# Vair

[Русский](README.md) | **English** | [فارسی](README_FA.md) | [中文](README_ZH.md)

[![Download](https://img.shields.io/github/downloads/lm705/vair/total?label=Download&logo=github)](https://github.com/lm705/vair/releases)
[![Release](https://img.shields.io/github/v/release/lm705/vair?label=Release)](https://github.com/lm705/vair/releases/latest)
![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20Linux-blue)
[![License](https://img.shields.io/github/license/lm705/vair)](LICENSE)
[![Stars](https://img.shields.io/github/stars/lm705/vair?style=flat)](https://github.com/lm705/vair/stargazers)

> ⚠️ This is an AI translation and may be inaccurate. The [Russian version](README.md) is the source of truth.

A VPN client for testing and connecting to configurations of the **VLESS, VMess, Trojan, Shadowsocks (including SS2022), Hysteria2, TUIC, WireGuard** protocols, as well as ordinary **SOCKS5, HTTP and HTTPS** proxies. Built on top of [Xray-core](https://github.com/XTLS/Xray-core) and [sing-box](https://github.com/SagerNet/sing-box).

**You can start without configurations of your own:** a list of free ones is already built in — it is refreshed every hour and checked automatically. In [**AUTO**](https://github.com/lm705/vair/wiki/Авто-подключение) mode the app tests the configurations itself, connects to the fastest working one and switches to another if it stops responding. A tab's auto-refresh then keeps the list itself current — fresh configurations arrive without you doing anything.

> ℹ️ **Note:** that built-in list is collected and tested **inside Russia only**, so it may not work in your country. In that case add your own subscriptions and configurations — everything below works exactly the same with them.

Your own configurations are just as welcome: **subscription links**, files and individual configurations sort into tabs and work with the same testing, routing and AUTO.

**Windows and Linux.** On Windows: the `Vair_<version>_setup.exe` installer, which installs the program for all users together with a small helper service, after which Vair runs without administrator rights, TUN mode and the kill-switch included; or the portable `Vair.exe` with the engines inside (no installation; TUN needs it run as administrator). On Linux: `.deb`, `.rpm` and Arch packages, with the engines installed and updated alongside.

<a href="screenshots/main-window.png"><img src="screenshots/main-window.png" alt="Vair main window: the configuration table with test results" width="900"></a> <a href="screenshots/auto-panel.png"><img src="screenshots/auto-panel.png" alt="The AUTO panel: the connection, the candidates and the IP check" width="450"></a>

<details>
<summary><b>More screenshots</b></summary>

<br>

**AUTO leaves a server that stopped answering (illustration)**

<img src="screenshots/auto-switch.gif" alt="Illustration: AUTO notices that a server does not answer and switches to the next one" width="456">

**Settings: the Tunnel section and the kill-switch description**

<a href="screenshots/settings.png"><img src="screenshots/settings.png" alt="Settings, the Tunnel section, the kill-switch description open" width="720"></a>

**The Connections panel: which programs go where through Vair**

<a href="screenshots/connections.png"><img src="screenshots/connections.png" alt="The Connections panel: programs, destinations and traffic through the tunnel" width="900"></a>

**A configuration's measurement history**

<a href="screenshots/history.png"><img src="screenshots/history.png" alt="Measurement history: delay, speed and the latest checks" width="520"></a>

</details>

## Feedback

* News, releases and test builds: the Telegram channel [**t.me/vair705**](https://t.me/vair705). *(in Russian)*
* Please post suggestions, bugs and any feedback in [**Issues**](https://github.com/lm705/vair/issues). Feel free to write in whatever language is convenient for you.
* To reach me directly: Telegram [**t.me/mial705**](https://t.me/mial705).

## Quick start

1. Download from the [**Releases**](https://github.com/lm705/vair/releases) page: `Vair_<version>_setup.exe` (the installer) or `Vair.exe` (portable, runs with no installation).

   On Linux, install the downloaded package:

   ```bash
   sudo apt install ./vair_*_amd64.deb             # Debian, Ubuntu, Kali
   sudo dnf install ./vair-*.x86_64.rpm            # Fedora
   sudo pacman -U ./vair-*-x86_64.pkg.tar.zst      # Arch
   ```

   The Windows files are not signed, so on the first launch Windows may show “Windows protected your PC”: click “More info” → “Run anyway”.

2. Open the **SOURCES** tab and press **RELOAD** — a fresh list of configurations is fetched.
3. Press **PING ALL** or **SPEED ALL** to check them and connect to any working one — or turn on **AUTO** and the app will do it for you.

More in [Getting started](https://github.com/lm705/vair/wiki/Начало-работы) in the Wiki.

## System requirements

* **Windows 10 or 11**, 64-bit, with the WebView2 Runtime: built into Windows 11, and the installer adds it when it is missing.
* **Linux x86_64** with GTK 4.10+ and WebKitGTK 6.0: Ubuntu 24.04+, Debian 13+, recent Fedora and Arch, Kali. Ubuntu 22.04 will not do.
* The xray-core and sing-box engines come with the program.

## Features

* **Protocols** — VLESS / VMess / Trojan / Shadowsocks / SS2022 / Hysteria2 / TUIC / WireGuard, plus SOCKS5, HTTP and HTTPS proxies, with TLS and Reality.
* **Two modes** — system proxy (HTTP/SOCKS) and TUN: all traffic through the tunnel.
* [**Auto-connect (AUTO)**](https://github.com/lm705/vair/wiki/Авто-подключение) — connects to the fastest working configuration and switches over when it stops answering.
* **Testing** — latency and speed per configuration, in bulk too; results persist between launches and build a history. Plus a check of which services open from which server.
* **Routing** — five modes: everything through the VPN; everything except the sites of selected countries (🇷🇺 🇨🇳 🇮🇷 🇰🇿); only what is blocked in Russia; only what is blocked in China; only selected sites. Plus your own rules for domains, IPs and apps.
* **Leak protection** — a kill-switch and DNS leak protection in TUN mode, on Windows and Linux alike; a block that outlives the program being closed (Windows); a leak check after every connection (Windows); a choice of what happens to IPv6.
* **You can see what is in the tunnel** — the connections panel: program, destination, the rule that applied and the traffic, with a separate table for what goes around the VPN.
* **Chains and DPI bypass** — connect through several nodes in a row; TLS handshake fragmentation and a browser TLS fingerprint (uTLS).
* **Tabs and sources** — subscription links, files and a private GitHub repository, with auto-refresh and filters; lists of up to ~250,000 configurations stay smooth (SQLite).
* **VPN on your phone** — share the proxy over the local network from TUN mode and [control the app from a phone](https://github.com/lm705/pc-vpn-to-phone).
* **Conveniences** — QR codes, creating configurations by hand, raw Xray and sing-box JSON included, self-update, autostart, dark and light themes, Russian and English interface.

## Documentation

* [**Wiki**](https://github.com/lm705/vair/wiki) — the full manual: settings, modes, routing, chains, testing, FAQ. *(in Russian)*
* 📱 [**How to use this PC's VPN on your phone**](https://github.com/lm705/pc-vpn-to-phone) — sharing over Wi-Fi and access from anywhere via Tailscale.

## vless_alive.txt and all_alive.txt

A list of free VLESS configurations that work within Russia.

Preloaded in the **SOURCES** tab of the application. The **RELOAD** button fetches the latest version of the file.

`all_alive.txt` differs in that it holds configurations of every protocol: VLESS, VMess, Trojan, Shadowsocks, Hysteria2 and TUIC.

Configurations are updated every hour, passing automatic availability and speed checks on a server located in one of the most heavily censored regions of Russia. Unavailable and slow configurations are filtered out. Most configurations are functional at the time of each update.

> ⚠️ **From 19 October 2026 the list is no longer updated hourly.** I have moved (and I recommend the same — there is less time than it seems), so I can no longer keep up a regular schedule. Someone else will keep updating it in their free time, without a fixed cadence. The availability and speed checks stay, but the configurations may be less fresh than before — it is worth adding sources of your own as well.

Configurations are collected from open public sources. Collection and automatic checking are done by the [vair-collector](https://github.com/lm705/vair-collector) script.

The country listed in the name may differ from the actual exit IP.

## Disclaimer

This software is provided "as is", without any express or implied warranties.

The program is a technical tool for working with network protocols. **This is not advertising, promotion, or provision of VPN services.** The program is not a VPN provider, does not include its own servers, and does not provide services for accessing blocked resources.

The `vless_alive.txt` and `all_alive.txt` files contain configurations collected from publicly available sources on the Internet. The author is not the owner or operator of, and does not control, the servers listed in the configurations. Availability, security, and privacy when using these servers are not guaranteed.

The user bears full and sole responsibility for compliance with the laws of their jurisdiction when using this software.

>## Support
>
>[DALINK](https://dalink.to/lm705) (open it without a VPN for prices to show in RUB)
>
>The "payment" address information is provided for informational purposes only.
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

## Repository statistics

<a href="https://www.star-history.com/?repos=lm705%2Fvair&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=lm705/vair&type=date&theme=dark&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=lm705/vair&type=date&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=lm705/vair&type=date&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
 </picture>
</a>
