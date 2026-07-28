# Vair

**Русский** | [English](README_EN.md) | [فارسی](README_FA.md) | [中文](README_ZH.md)

GUI клиент для проверки и подключения к VPN-конфигурациям протоколов **VLESS, VMess, Trojan, Shadowsocks (включая SS2022), Hysteria2, TUIC**. Построен на базе [Xray-core](https://github.com/XTLS/Xray-core) и [sing-box](https://github.com/SagerNet/sing-box).

**Можно начать без своих конфигураций:** внутри уже есть список бесплатных конфигураций, который обновляется каждый час и проходит автопроверку. В режиме [**AUTO**](https://github.com/lm705/vair/wiki/Авто-подключение) программа сама тестирует конфигурации, подключается к самой быстрой рабочей и переключается на другую, если та перестала отвечать. А авто-обновление вкладки будет держать сам список конфигураций актуальным — свежие подтянутся без вашего участия.

Свои конфигурации тоже никуда не деваются: добавляйте **подписки по ссылке**, файлы и отдельные конфигурации — раскладывайте их по вкладкам и пользуйтесь теми же тестами, маршрутизацией и AUTO.

Только на **Windows**. Один файл `.exe`, установка не требуется — движки xray-core и sing-box уже внутри.

> 💡 Предложения, ошибки и любые пожелания, пожалуйста, пишите в [**Issues**](https://github.com/lm705/vair/issues). Писать можно на удобном вам языке.

[![Download](https://img.shields.io/github/downloads/lm705/vair/total?label=Download&logo=github)](https://github.com/lm705/vair/releases)
[![Release](https://img.shields.io/github/v/release/lm705/vair?label=Release)](https://github.com/lm705/vair/releases/latest)
![Platform](https://img.shields.io/badge/platform-Windows-blue)
[![Stars](https://img.shields.io/github/stars/lm705/vair?style=flat)](https://github.com/lm705/vair/stargazers)

<a href="screenshots/main-window.png"><img src="screenshots/main-window.png" alt="Главное окно Vair: таблица конфигураций с результатами тестов" width="731"></a> <a href="screenshots/auto-panel.png"><img src="screenshots/auto-panel.png" alt="Панель AUTO и настройки маршрутизации" width="274"></a>

## Быстрый старт

1. Скачайте `Vair.exe` со страницы [**Releases**](https://github.com/lm705/vair/releases) и запустите — установка не нужна.
2. Откройте вкладку **SOURCES** и нажмите **RELOAD** — загрузится свежий список конфигураций.
3. Нажмите **PING ALL** или **SPEED ALL**, чтобы проверить их, и подключитесь к любой рабочей — или включите **AUTO**, и программа сделает это сама.

Подробнее — [Начало работы](https://github.com/lm705/vair/wiki/Начало-работы) в Wiki.

## Возможности

* **Множество протоколов** — VLESS / VMess / Trojan / Shadowsocks / SS2022 / Hysteria2 / TUIC, с TLS и Reality.
* **Два режима** — системный прокси (HTTP/SOCKS) и TUN (весь трафик через туннель).
* [**Авто-подключение (AUTO)**](https://github.com/lm705/vair/wiki/Авто-подключение) — сама подключается к самому быстрому рабочему конфигу и переключается, если тот перестал работать.
* **Тестирование** — задержка (ping) и скорость загрузки по каждому конфигу, в том числе пакетно; результаты сохраняются между запусками.
* **Гибкая маршрутизация** — пять режимов: весь трафик через VPN; всё, кроме сайтов выбранных стран (🇷🇺 🇨🇳 🇮🇷 🇰🇿); только заблокированное в РФ; только заблокированное в Китае; только выбранные сайты. Плюс свои правила для доменов, IP и приложений.
* **Цепочки** — соединение через несколько узлов подряд (вы → узел A → узел B → интернет).
* **Обход DPI** — фрагментация TLS-рукопожатия и выбор браузерного TLS-отпечатка (uTLS).
* **Защита от утечек DNS** и kill-switch в TUN-режиме.
* **Вкладки и источники** — конфиги раскладываются по вкладкам, подгружаются из подписок, файлов или приватного репозитория GitHub, с авто-обновлением и фильтрами.
* **Большие списки** — до ~250 000 конфигураций работают плавно: хранение в SQLite, мгновенные сортировка, поиск и фильтр.
* **VPN на телефоне** — раздача прокси по локальной сети из TUN-режима и [управление программой с телефона](https://github.com/lm705/pc-vpn-to-phone).
* **Удобства** — QR-коды, создание конфигураций вручную, самообновление, автозапуск, тёмная и светлая темы, русский и английский интерфейс.

## Документация

* [**Wiki**](https://github.com/lm705/vair/wiki) — полное руководство: настройки, режимы, маршрутизация, цепочки, тестирование, FAQ.
* 📱 [**Как пользоваться VPN этого ПК на телефоне**](https://github.com/lm705/pc-vpn-to-phone) — раздача по Wi-Fi и доступ из любой точки через Tailscale.

## vless_alive.txt

Список бесплатных VLESS-конфигураций, работающих на территории России.

Предзагружен во вкладку **SOURCES** программы. Кнопка **RELOAD** подгружает свежую версию файла.

Конфигурации обновляются каждый час, проходя автоматическую проверку на доступность и скорость на сервере в одном из самых глухих в плане блокировок регионов России. Недоступные и медленные конфигурации отфильтровываются. Большинство конфигураций работоспособны на момент обновления.

Конфигурации собираются из открытых источников. Сбор и автоматическая проверка выполняются скриптом [vair-collector](https://github.com/lm705/vair-collector).

Страна в названии может отличаться от выходного IP.

## Дисклеймер

Данное программное обеспечение предоставляется «как есть» (*as is*), без каких-либо явных или подразумеваемых гарантий.

Программа является техническим инструментом для работы с сетевыми протоколами. **Это не реклама, не продвижение и не предоставление VPN-услуг.** Программа не является VPN-провайдером, не содержит собственных серверов и не оказывает услуг по обеспечению доступа к заблокированным ресурсам.

Файл `vless_alive.txt` содержит конфигурации, собранные из открытых источников в сети Интернет. Автор не является владельцем, оператором и не контролирует серверы, указанные в конфигурациях. Доступность, безопасность и конфиденциальность при использовании этих серверов не гарантируется.

Пользователь самостоятельно несёт полную ответственность за соблюдение законодательства своей юрисдикции при использовании данного ПО.

>## Поддержка
>
>[DALINK](https://dalink.to/lm705) (для отображения валюты RUB заходите без VPN)
>
>Информация о "платежных" адресах размещена в ознакомительных целях. 
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

## Статистика репозитория

<a href="https://www.star-history.com/?repos=lm705%2Fvair&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=lm705/vair&type=date&theme=dark&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=lm705/vair&type=date&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=lm705/vair&type=date&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
 </picture>
</a>
