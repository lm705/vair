# Vair

**Русский** | [English](README_EN.md) | [فارسی](README_FA.md) | [中文](README_ZH.md)

[![Download](https://img.shields.io/github/downloads/lm705/vair/total?label=Download&logo=github)](https://github.com/lm705/vair/releases)
[![Release](https://img.shields.io/github/v/release/lm705/vair?label=Release)](https://github.com/lm705/vair/releases/latest)
![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20Linux-blue)
[![License](https://img.shields.io/github/license/lm705/vair)](LICENSE)
[![Stars](https://img.shields.io/github/stars/lm705/vair?style=flat)](https://github.com/lm705/vair/stargazers)

Vair — VPN-клиент для Windows и Linux. Проверяет и подключает конфигурации протоколов **VLESS, VMess, Trojan, Shadowsocks (включая SS2022), Hysteria2, TUIC, WireGuard**, а также обычные прокси **SOCKS5, HTTP и HTTPS**. Работает на движках [Xray-core](https://github.com/XTLS/Xray-core) и [sing-box](https://github.com/SagerNet/sing-box).

**Можно начать без своих конфигураций:** во вкладке **SOURCES** уже есть список бесплатных конфигураций, который регулярно обновляется и проходит автопроверку. В режиме [**AUTO**](https://github.com/lm705/vair/wiki/Авто-подключение) программа сама тестирует конфигурации, подключается к самой быстрой рабочей и переключается на другую, если та перестала отвечать. Обновить список можно кнопкой **RELOAD**.

Можно добавить и свои: **подписки по ссылке**, файлы и отдельные конфигурации раскладываются по вкладкам, и для них работают те же тесты, маршрутизация и AUTO.

**Windows и Linux:**

* **Windows, установщик** `Vair_<версия>_setup.exe` — ставит программу для всех пользователей вместе с небольшой службой-помощником. После этого Vair работает без прав администратора, включая режим TUN и kill-switch.
* **Windows, портативный** `Vair.exe` — запускается без установки, движки внутри. Для режима TUN его нужно запускать от имени администратора.
* **Linux** — пакеты `.deb`, `.rpm` и Arch; движки ставятся и обновляются вместе с пакетом.

<a href="screenshots/main-window.png"><img src="screenshots/main-window.png" alt="Главное окно Vair: таблица конфигураций с результатами тестов" width="900"></a> <a href="screenshots/auto-panel.png"><img src="screenshots/auto-panel.png" alt="Панель AUTO: подключение, кандидаты и проверка IP" width="450"></a>

<details>
<summary><b>Ещё скриншоты</b></summary>

<br>

**AUTO уходит с сервера, который перестал отвечать (иллюстрация)**

<img src="screenshots/auto-switch.gif" alt="Иллюстрация: AUTO замечает, что сервер не отвечает, и переключается на следующий" width="456">

**Настройки: раздел Tunnel и описание kill-switch**

<a href="screenshots/settings.png"><img src="screenshots/settings.png" alt="Настройки, раздел Tunnel, открыто описание kill-switch" width="720"></a>

**Панель Connections: какие программы и куда ходят через Vair**

<a href="screenshots/connections.png"><img src="screenshots/connections.png" alt="Панель Connections: программы, адреса и трафик через туннель" width="900"></a>

**История замеров конфигурации**

<a href="screenshots/history.png"><img src="screenshots/history.png" alt="История замеров: задержка, скорость и последние проверки" width="520"></a>

</details>

## Обратная связь

* **Новости и релизы** — Telegram-канал [**t.me/vair705**](https://t.me/vair705). Там же будут тестовые сборки 3.0.0.
* **Ошибки и предложения** — в [**Issues**](https://github.com/lm705/vair/issues), на любом удобном языке.
* **Написать лично** — Telegram [**t.me/mial705**](https://t.me/mial705).
* **Обсуждение** — чат [**t.me/vair705chat**](https://t.me/vair705chat).

## Быстрый старт

1. Скачайте со страницы [**Releases**](https://github.com/lm705/vair/releases): на Windows — `Vair_<версия>_setup.exe` (установщик) или `Vair.exe` (портативный, запускается без установки); на Linux — пакет для вашего дистрибутива (`.deb`, `.rpm` или `.pkg.tar.zst`).

   На Linux поставьте скачанный пакет:

   ```bash
   sudo apt install ./vair_*_amd64.deb             # Debian, Ubuntu, Kali
   sudo dnf install ./vair-*.x86_64.rpm            # Fedora
   sudo pacman -U ./vair-*-x86_64.pkg.tar.zst      # Arch
   ```

   Файлы для Windows не подписаны, поэтому при первом запуске Windows может показать окно «Система Windows защитила ваш компьютер» (Windows protected your PC): нажмите «Подробнее» → «Выполнить в любом случае».

2. Откройте вкладку **SOURCES** и нажмите **RELOAD** — загрузится свежий список конфигураций.
3. Нажмите **PING ALL** или **SPEED ALL**, чтобы проверить их, и подключитесь к любой рабочей — или включите **AUTO**, и программа сделает это сама.

Подробнее — [Начало работы](https://github.com/lm705/vair/wiki/Начало-работы) в Wiki.

## Системные требования

* **Windows 10 или 11**, 64-битная, с WebView2 Runtime: в Windows 11 он встроен, а если его нет, установщик поставит его сам.
* **Linux x86_64** с GTK 4.10+ и WebKitGTK 6.0: Ubuntu 24.04+, Debian 13+, свежие Fedora и Arch, Kali. Ubuntu 22.04 не подойдёт.
* Движки xray-core и sing-box поставляются вместе с программой.

## Возможности

* **Протоколы.** VLESS, VMess, Trojan, Shadowsocks (включая SS2022), Hysteria2, TUIC и WireGuard, а также прокси SOCKS5, HTTP и HTTPS. Поддерживаются TLS и REALITY.
* **Режимы подключения.** Системный прокси (HTTP/SOCKS) или TUN — перехват всего трафика системы.
* [**Авто-подключение (AUTO)**](https://github.com/lm705/vair/wiki/Авто-подключение). Выбирает самую быструю рабочую конфигурацию и переключается на другую, если сервер перестал отвечать.
* **Тестирование.** Задержка и скорость каждой конфигурации — по одной или пакетно. Результаты сохраняются между запусками и накапливаются в истории замеров. Отдельная проверка показывает, какие сервисы доступны через каждый сервер.
* **Маршрутизация.** Пять режимов: весь трафик через VPN; всё, кроме сайтов выбранных стран (🇷🇺 🇨🇳 🇮🇷 🇰🇿); только ресурсы, заблокированные в России; только ресурсы, заблокированные в Китае; только выбранные сайты. Дополнительно — собственные правила для доменов, IP-адресов и приложений.
* **Защита от утечек.** Kill-switch и защита DNS в режиме TUN, проверка на утечки после каждого подключения — на Windows и Linux. На Windows также блокировка, которая сохраняется после закрытия программы. Обработка IPv6 настраивается отдельно.
* **Мониторинг соединений.** Панель соединений показывает программу, адрес назначения, применённое правило и объём трафика; соединения в обход VPN выводятся отдельной таблицей.
* **Цепочки и обход DPI.** Подключение через несколько серверов последовательно; фрагментация TLS-рукопожатия и браузерный TLS-отпечаток (uTLS).
* **Управление конфигурациями.** Вкладки, подписки по ссылке, файлы и приватные репозитории GitHub, обновление по расписанию и фильтры. Списки до ~250 000 конфигураций работают без заметных задержек (хранение в SQLite).
* **Доступ с телефона.** Раздача прокси в локальной сети из режима TUN и [управление программой с телефона](https://github.com/lm705/pc-vpn-to-phone).
* **Прочее.** QR-коды; создание конфигураций вручную, в том числе из JSON для Xray и sing-box; проверка и загрузка обновлений; автозапуск; тёмная и светлая темы; интерфейс на русском и английском.

## Документация

* [**Wiki**](https://github.com/lm705/vair/wiki) — полное руководство: настройки, режимы, маршрутизация, цепочки, тестирование, FAQ.
* 📱 [**Как пользоваться VPN этого ПК на телефоне**](https://github.com/lm705/pc-vpn-to-phone) — раздача по Wi-Fi и доступ из любой точки через Tailscale.

## vless_alive.txt и all_alive.txt

Список бесплатных VLESS-конфигураций, работающих на территории России.

Предзагружен во вкладку **SOURCES** программы. Кнопка **RELOAD** подгружает свежую версию файла.

`all_alive.txt` отличается тем, что в нём собраны конфигурации всех протоколов: VLESS, VMess, Trojan, Shadowsocks, Hysteria2 и TUIC.

Конфигурации обновляются каждый час, проходя автоматическую проверку на доступность и скорость на сервере в одном из самых глухих в плане блокировок регионов России. Недоступные и медленные конфигурации отфильтровываются. Большинство конфигураций работоспособны на момент обновления.

> ⚠️ **С 19 октября 2026 года список перестаёт обновляться каждый час.** Я сменил местоположение (и вам рекомендую — времени меньше, чем кажется), поэтому больше не могу поддерживать регулярное обновление. Список продолжит обновлять другой человек — по мере свободного времени, без расписания. Проверка на доступность и скорость сохраняется, но конфигурации могут быть менее свежими, чем раньше, поэтому имеет смысл добавить и свои источники.

Конфигурации собираются из открытых источников. Сбор и автоматическая проверка выполняются скриптом [vair-collector](https://github.com/lm705/vair-collector).

Страна в названии может отличаться от выходного IP.

## Дисклеймер

Данное программное обеспечение предоставляется «как есть» (*as is*), без каких-либо явных или подразумеваемых гарантий.

Программа является техническим инструментом для работы с сетевыми протоколами. **Это не реклама, не продвижение и не предоставление VPN-услуг.** Программа не является VPN-провайдером, не содержит собственных серверов и не оказывает услуг по обеспечению доступа к заблокированным ресурсам.

Файлы `vless_alive.txt` и `all_alive.txt` содержат конфигурации, собранные из открытых источников в сети Интернет. Автор не является владельцем, оператором и не контролирует серверы, указанные в конфигурациях. Доступность, безопасность и конфиденциальность при использовании этих серверов не гарантируется.

Пользователь самостоятельно несёт полную ответственность за соблюдение законодательства своей юрисдикции при использовании данного ПО.

>## Поддержка
>
>[DALINK](https://dalink.to/lm705) (для отображения валюты RUB заходите без VPN)
>
>Информация о "платежных" адресах размещена в ознакомительных целях. 
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

## Статистика репозитория

<a href="https://www.star-history.com/?repos=lm705%2Fvair&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=lm705/vair&type=date&theme=dark&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=lm705/vair&type=date&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=lm705/vair&type=date&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
 </picture>
</a>
