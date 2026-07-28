# Vair

[Русский](README.md) | **English** | [فارسی](README_FA.md) | [中文](README_ZH.md)

> ⚠️ This is an AI translation and may be inaccurate. The [Russian version](README.md) is the source of truth.

A client for testing and connecting to VPN configurations supporting the **VLESS, VMess, Trojan, Shadowsocks (including SS2022), Hysteria2, TUIC** protocols. Built on top of [Xray-core](https://github.com/XTLS/Xray-core) and [sing-box](https://github.com/SagerNet/sing-box).

**You can start without configurations of your own:** a list of free ones is already built in — it is refreshed every hour and checked automatically. In [**AUTO**](https://github.com/lm705/vair/wiki/Авто-подключение) mode the app tests the configurations itself, connects to the fastest working one and switches to another if it stops responding. A tab's auto-refresh then keeps the list itself current — fresh configurations arrive without you doing anything.

> ℹ️ **Note:** that built-in list is collected and tested **inside Russia only**, so it may not work in your country. In that case add your own subscriptions and configurations — everything below works exactly the same with them.

Your own configurations are just as welcome: add **subscription links**, files and individual configurations — sort them into tabs and use the same testing, routing and AUTO.

**Windows** only. A single `.exe`, no installation needed — the xray-core and sing-box engines are already inside.

> 💡 Please post suggestions, bugs and any feedback in [**Issues**](https://github.com/lm705/vair/issues). Feel free to write in whatever language is convenient for you.

[![Download](https://img.shields.io/github/downloads/lm705/vair/total?label=Download&logo=github)](https://github.com/lm705/vair/releases)
[![Release](https://img.shields.io/github/v/release/lm705/vair?label=Release)](https://github.com/lm705/vair/releases/latest)
![Platform](https://img.shields.io/badge/platform-Windows-blue)
[![Stars](https://img.shields.io/github/stars/lm705/vair?style=flat)](https://github.com/lm705/vair/stargazers)

<a href="screenshots/main-window.png"><img src="screenshots/main-window.png" alt="Vair main window: the configuration table with test results" width="900"></a> <a href="screenshots/auto-panel.png"><img src="screenshots/auto-panel.png" alt="The AUTO panel and routing settings" width="450"></a>

## Quick start

1. Download `Vair.exe` from the [**Releases**](https://github.com/lm705/vair/releases) page and run it — no installation required.
2. Open the **SOURCES** tab and press **RELOAD** — a fresh list of configurations is fetched.
3. Press **PING ALL** or **SPEED ALL** to check them and connect to any working one — or turn on **AUTO** and the app will do it for you.

More in [Getting started](https://github.com/lm705/vair/wiki/Начало-работы) in the Wiki.

## Features

* **Many protocols** — VLESS / VMess / Trojan / Shadowsocks / SS2022 / Hysteria2 / TUIC, with TLS and Reality.
* **Two modes** — system proxy (HTTP/SOCKS) and TUN (all traffic through the tunnel).
* [**Auto-connect (AUTO)**](https://github.com/lm705/vair/wiki/Авто-подключение) — connects to the fastest working config by itself and switches over when it stops working.
* **Testing** — latency (ping) and download speed per configuration, in bulk too; results persist between launches.
* **Flexible routing** — five modes: all traffic through the VPN; everything except the sites of selected countries (🇷🇺 🇨🇳 🇮🇷 🇰🇿); only what's blocked in Russia; only what's blocked in China; only selected sites. Plus your own rules for domains, IPs and apps.
* **Chains** — connect through several nodes in a row (you → node A → node B → the internet).
* **DPI bypass** — TLS handshake fragmentation and a browser TLS fingerprint (uTLS).
* **DNS leak protection** and a kill-switch in TUN mode.
* **Tabs and sources** — configurations are organised into tabs and pulled from subscriptions, files or a private GitHub repository, with auto-refresh and filters.
* **Large lists** — up to ~250,000 configurations stay smooth: SQLite storage, instant sorting, search and filtering.
* **VPN on your phone** — share the proxy over the local network from TUN mode and [control the app from a phone](https://github.com/lm705/pc-vpn-to-phone).
* **Conveniences** — QR codes, creating configurations by hand, self-update, autostart, dark and light themes, Russian and English interface.

## Documentation

* [**Wiki**](https://github.com/lm705/vair/wiki) — the full manual: settings, modes, routing, chains, testing, FAQ. *(in Russian)*
* 📱 [**How to use this PC's VPN on your phone**](https://github.com/lm705/pc-vpn-to-phone) — sharing over Wi-Fi and access from anywhere via Tailscale.

## vless_alive.txt

A list of free VLESS configurations that work within Russia.

Preloaded in the **SOURCES** tab of the application. The **RELOAD** button fetches the latest version of the file.

Configurations are updated every hour, passing automatic availability and speed checks on a server located in one of the most heavily censored regions of Russia. Unavailable and slow configurations are filtered out. Most configurations are functional at the time of each update.

Configurations are collected from open public sources. Collection and automatic checking are done by the [vair-collector](https://github.com/lm705/vair-collector) script.

The country listed in the name may differ from the actual exit IP.

## Disclaimer

This software is provided "as is", without any express or implied warranties.

The program is a technical tool for working with network protocols. **This is not advertising, promotion, or provision of VPN services.** The program is not a VPN provider, does not include its own servers, and does not provide services for accessing blocked resources.

The `vless_alive.txt` file contains configurations collected from publicly available sources on the Internet. The author is not the owner or operator of, and does not control, the servers listed in the configurations. Availability, security, and privacy when using these servers are not guaranteed.

The user bears full and sole responsibility for compliance with the laws of their jurisdiction when using this software.

>## Support
>
>[DALINK](https://dalink.to/lm705) (open it without a VPN for prices to show in RUB)
>
>The "payment" address information is provided for informational purposes only.
>
>ERC20:
>
>*0x37072eb42019a258a801df002e5c8d5542709165*
>
>TRC20:
>
>*TGz3wkGFL7PNjSoxpDnM5WgXEvod3qfz3k*
>
>SOL:
>
>*F7pVS8N74ARDJYmiTqFeViRVHZhXJ8toJsxyrmWstfmi*
>
>TON:
>
>*UQBxGaG0hBVKarg6TJVaqvCN0hOFAodQjnCZ9rdnUaWxcbDV*

## Repository statistics

<a href="https://www.star-history.com/?repos=lm705%2Fvair&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=lm705/vair&type=date&theme=dark&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=lm705/vair&type=date&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=lm705/vair&type=date&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
 </picture>
</a>
