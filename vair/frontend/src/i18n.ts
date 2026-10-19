// Vair 2.0 i18n — the VERBATIM 1.10 dictionary (web/index.html I18N.ru).
// Keys ARE the English text (doubling as the fallback when no translation
// exists). The main window stays English on purpose — table headers, status
// pills, tab bar and titlebar are untouched; modals/menus/chips translate.
export const RU: Record<string, string> = {
    // Section headers
    "Settings": "Настройки",
    // "Sources" is intentionally NOT translated — the user asked to keep
    // the section header as-is so the tab name and the settings section
    // stay visually consistent across languages.
    "Routing": "Маршрутизация",
    "Testing": "Тестирование",
    "Ports": "Порты",
    "HTTP proxy port": "Порт HTTP-прокси",
    "SOCKS proxy port": "Порт SOCKS-прокси",
    "Allow LAN access": "Доступ из локальной сети",
    "Network": "Сеть",
    "Statistics": "Статистика",
    "Security": "Безопасность",
    "DNS": "DNS",
    "System": "Система",
    "Appearance": "Внешний вид",

    // Settings — labels / buttons
    "Enable Sources tab": "Включить вкладку «SOURCES»",
    "Routing mode": "Режим маршрутизации",
    "All traffic through VPN": "Весь трафик через VPN",
    "Only blocked-in-Russia resources": "Только заблокированное в РФ",
    "Only selected sites": "Только выбранные сайты",
    "Everything except selected countries": "Всё, кроме сайтов выбранных стран",
    "Sites of these countries go direct (bypass the VPN):": "Сайты этих стран идут напрямую (мимо VPN):",
    "Russian sites": "Российские сайты",
    "Chinese sites": "Китайские сайты",
    "Iranian sites": "Иранские сайты",
    "Kazakh sites": "Казахстанские сайты",
    "Local sites of these countries often refuse foreign/VPN addresses (banks, government services) — routing them direct keeps them working while everything else rides the VPN. Each country is matched by its TLD zone (*.ru, *.cn, *.ir, *.kz), its known-domains list and its IP ranges. With nothing selected, everything goes through the VPN. Takes effect on next connection.":
      "Локальные сайты этих стран часто не пускают с зарубежных/VPN-адресов (банки, госуслуги) — прямой маршрут сохраняет их работу, а всё остальное идёт через VPN. Страна определяется по её доменной зоне (*.ru, *.cn, *.ir, *.kz), списку известных доменов и IP-диапазонам. Если ничего не выбрано — весь трафик идёт через VPN. Применяется при следующем подключении.",
    "Only blocked-in-China resources": "Только заблокированное в Китае",
    "Custom rules — without VPN": "Свои правила — без VPN",
    "Custom rules — through VPN": "Свои правила — через VPN",
    "Custom rules — through VPN (URL)": "Свои правила — через VPN (по ссылке)",
    "https://…/domains.txt — press Enter": "https://…/domains.txt — Enter для добавления",
    "A URL-backed version of the list above: one or more links to plain-text domain lists (one domain per line), fetched, auto-updated and routed THROUGH the VPN. Handy for large or community-maintained lists instead of typing each domain. Press Enter to add a link. Takes effect on next connection.":
      "Версия списка выше по ссылке: одна или несколько ссылок на текстовые списки доменов (по одному в строке), которые скачиваются, авто-обновляются и идут ЧЕРЕЗ VPN. Удобно для больших или поддерживаемых сообществом списков — вместо ввода каждого домена вручную. Enter — добавить ссылку. Применяется при следующем подключении.",
    "Custom rules — block": "Свои правила — блокировать",
    "domain, IP or CIDR — press Enter": "домен, IP или CIDR — Enter",
    "Kept OFF the VPN (direct). Accepts a domain (all subdomains included), an IP or a CIDR (e.g. 10.0.0.0/8), or an advanced prefix — full:, regexp:, geosite:, geoip: (the geosite:/geoip: categories work in proxy mode only). Takes effect on next connection.":
      "Идут мимо VPN (напрямую). Принимается домен (все поддомены включаются), IP или CIDR (например, 10.0.0.0/8), либо продвинутый префикс — full:, regexp:, geosite:, geoip: (категории geosite:/geoip: работают только в режиме proxy). Применяется при следующем подключении.",
    "Forced THROUGH the VPN — even in a bypass mode. Same syntax as above. In whitelist mode (Only selected sites) this is the main list. Takes effect on next connection.":
      "Принудительно идут ЧЕРЕЗ VPN — даже в режимах-исключениях. Синтаксис как выше. В режиме белого списка («Только выбранные сайты») это основной список. Применяется при следующем подключении.",
    "Dropped entirely (ads, telemetry, unwanted hosts). Same syntax as above. Highest priority: block wins over the without-VPN and through-VPN lists. Takes effect on next connection.":
      "Полностью блокируются (реклама, телеметрия, нежелательные хосты). Синтаксис как выше. Наивысший приоритет: блокировка важнее списков «без VPN» и «через VPN». Применяется при следующем подключении.",
    "Regular traffic goes DIRECT; only resources blocked in China go through the VPN — matched by the community GFW list (Loyalsoldier build, auto-updated), plus your own lists below.":
      "Обычный трафик идёт НАПРЯМУЮ; через VPN — только ресурсы, заблокированные в Китае: по комьюнити-списку GFW (сборка Loyalsoldier, автообновляется) плюс ваши списки ниже.",
    "Whitelist mode: ONLY the domains you list below go through the VPN — everything else stays on the direct connection. With an empty list, nothing is tunnelled. If \"Set system proxy\" is off, the app filter also applies (only apps pointed at the proxy, AND only these domains).":
      "Режим белого списка: через VPN идут ТОЛЬКО перечисленные ниже домены — всё остальное остаётся на прямом соединении. При пустом списке через туннель не идёт ничего. Если «Задавать системный прокси» выключено, добавляется и фильтр по приложениям (только приложения, указавшие прокси, И только эти домены).",
    "How traffic is split between the VPN and a direct connection. Takes effect on next connection.":
      "Как трафик распределяется между VPN и прямым подключением. Применяется при следующем подключении.",
    "Custom domains without VPN": "Свои домены без VPN",
    "TUN Apps without VPN": "TUN: приложения без VPN",
    "Browse running processes": "Просмотреть запущенные процессы",
    "Ping concurrency": "Параллельных ping-тестов",
    "Speed concurrency": "Параллельных speed-тестов",
    "How many candidates auto-connect tests at once — ping when ranking by delay, speed when ranking by speed. Defaults: ping 10, speed 5.":
      "Сколько кандидатов авто-подключение тестирует одновременно: ping при ранжировании по задержке, speed — при ранжировании по скорости. По умолчанию: ping 10, speed 5.",
    "These are the same settings as Settings → Testing: changing them here changes them there, and they apply to manual PING ALL / SPEED ALL runs too.":
      "Это те же настройки, что в «Настройки → Тестирование»: изменение здесь меняет их и там, и они действуют также на ручные PING ALL / SPEED ALL.",
    "Warm-up timeout (ms)": "Таймаут разогрева (мс)",
    "Ping timeout (ms)": "Таймаут ping (мс)",
    "Speed test duration (s)": "Длительность speed-теста (с)",
    "Ping URL": "URL для ping",
    "Custom ping URL": "Свой URL для ping",
    "Speed URL": "URL для speed",
    "Custom speed URL": "Свой URL для speed",
    "Speed URL fallback": "Резервный URL для speed",
    "(used when the main URL returns HTTP 429)": "(используется, если основной URL возвращает HTTP 429)",
    "Custom speed fallback URL": "Свой резервный URL для speed",
    "None — no fallback": "Без резерва",
    "Pick \"None\" to disable the retry.": "Выберите «Без резерва», чтобы отключить повтор.",
    "TUN MTU": "TUN MTU",
    "TLS fragmentation (DPI bypass)": "Фрагментация TLS (обход DPI)",
    "TLS fingerprint (uTLS)": "Отпечаток TLS (uTLS)",
    "Which browser's TLS handshake xray imitates (uTLS) when a config has no fp= of its own — without it the connection uses an easily-fingerprinted TLS signature. Applies to TLS/Reality nodes. Default: chrome. Takes effect on next connection.":
      "Под какой браузер xray маскирует TLS-рукопожатие (uTLS), если в конфиге нет своего fp= — без этого у соединения легко распознаваемый TLS-отпечаток. Действует для узлов TLS/Reality. По умолчанию: chrome. Применяется при следующем подключении.",
    "Splits the TLS handshake (ClientHello) into pieces so a DPI can't match the connection in a single packet. Helps when the server is alive but the handshake is being reset. xray protocols only (VLESS/VMess/Trojan/SS over TLS). Takes effect on next connection.":
      "Разбивает рукопожатие TLS (ClientHello) на части, чтобы DPI не мог распознать соединение в одном пакете. Помогает, когда сервер живой, но рукопожатие сбрасывается. Только протоколы xray (VLESS/VMess/Trojan/SS поверх TLS). Применяется при следующем подключении.",
    "Fragment length": "Длина фрагмента",
    "Fragment interval (ms)": "Интервал фрагментации (мс)",
    "Ranges as \"min-max\" (or a single number). Defaults: length 100-200, interval 10-20. Leave empty to use the defaults.":
      "Диапазоны в виде «min-max» (или одно число). По умолчанию: длина 100-200, интервал 10-20. Оставьте пустым для значений по умолчанию.",
    "Launch at system startup": "Запускать при старте системы",
    "Starts Vair automatically when you log in, minimized to the tray. Off by default.":
      "Автоматически запускает Vair при входе в систему, свёрнутым в трей. По умолчанию выключено.",
    "install helper ↗": "установить компонент ↗",
    "Installs a small system service that creates the tunnel. Asked once.":
      "Установит небольшую системную службу, которая создаёт туннель. Спросит один раз.",
    "not available on this system": "недоступно в этой системе",
    "Open releases": "Открыть релизы",
    "Download": "Скачать",
    "This copy was installed from a package — update it through your package manager.":
      "Эта копия установлена из пакета — обновляйте её через пакетный менеджер.",
    "This copy runs as an AppImage — download the new one and replace this file.":
      "Эта копия запущена как AppImage — скачайте новый и замените этот файл.",
    "Vair cannot open a port in the firewall on this system. If the phone cannot reach the address below, allow it once:":
      "Vair не может открыть порт в файрволе в этой системе. Если телефон не достучится по адресу ниже, разрешите его один раз:",
    "Handle vair:// links": "Обрабатывать ссылки vair://",
    "Registers the vair:// scheme so clicking a vair://import/… link (e.g. in a browser or Telegram) opens Vair and adds the subscription or config. On by default.":
      "Регистрирует схему vair://, чтобы клик по ссылке vair://import/… (например, в браузере или Telegram) открывал Vair и добавлял подписку или конфиг. По умолчанию включено.",
    "Added from link": "Добавлено по ссылке",
    "Link has no config or subscription": "В ссылке нет конфига или подписки",
    "Updates": "Обновления",
    "Check for updates": "Проверить обновления",
    "Checks for a newer build and installs it (downloads through the tunnel when connected). The download is verified by checksum before it replaces the app.":
      "Проверяет наличие новой версии и устанавливает её (при активном подключении скачивает через туннель). Загрузка проверяется по контрольной сумме перед заменой приложения.",
    "Update now": "Обновить сейчас",
    "New version": "Новая версия",
    "Checking…": "Проверка…",
    "You have the latest version.": "У вас последняя версия.",
    "Update available": "Доступно обновление",
    "New version available": "Доступна новая версия",
    "Update": "Обновить",
    "Don't show again": "Больше не показывать",
    "Close": "Закрыть",
    "Could not check for updates": "Не удалось проверить обновления",
    "Downloading update": "Загрузка обновления",
    "Verifying…": "Проверка целостности…",
    "Update ready — restarting…": "Обновление готово — перезапуск…",
    "Update failed": "Ошибка обновления",
    "Download and install the update now? Vair will restart.":
      "Скачать и установить обновление сейчас? Vair перезапустится.",
    "Enable traffic statistics": "Считать трафик",
    "Lifetime total": "Итого за всё время",
    "reset total": "сбросить итог",
    "SOCKS authentication": "Аутентификация SOCKS",
    "Protects the local SOCKS5 proxy (proxy mode) with a username/password so other local apps can't use it or probe your VPN server. Off by default; turn it on to require credentials. Takes effect on next connection.":
      "Защищает локальный SOCKS5-прокси (режим proxy) логином/паролем, чтобы другие локальные приложения не могли им воспользоваться или определить ваш VPN-сервер. По умолчанию выключено; включите, чтобы требовать логин/пароль. Применяется при следующем подключении.",
    "SOCKS username": "Логин SOCKS",
    "SOCKS password": "Пароль SOCKS",
    "Generate new credentials": "Сгенерировать новые данные",
    "Reset": "Сбросить",
    "Enter these in your SOCKS5 client. Reset generates new random credentials.":
      "Введите эти данные в SOCKS5-клиенте. «Сбросить» создаёт новые случайные.",
    "Sources added": "Источников добавлено",
    "Imported": "Импортировано",
    "Source unavailable": "Источник недоступен",
    "Retry": "Повторить",
    "Tab settings": "Настройки вкладки",
    "Copy the connected config": "Скопировать подключённый конфиг",
    "copied": "скопировано",
    "No configs match the filter": "Нет конфигов, подходящих под фильтр",
    "Clear filter": "Сбросить фильтр",
    "HTTP authentication": "Аутентификация HTTP",
    "Protects the local HTTP proxy with a username/password (Basic auth). Applies to BOTH proxy mode and the TUN “Share proxy over LAN” HTTP proxy. Off by default. Takes effect on next connection.":
      "Защищает локальный HTTP-прокси логином/паролем (Basic-авторизация). Действует и в режиме proxy, и для HTTP-прокси функции «Раздавать прокси по LAN в TUN-режиме». По умолчанию выключено. Применяется при следующем подключении.",
    "HTTP username": "Логин HTTP",
    "HTTP password": "Пароль HTTP",
    "Enter these in your HTTP client. Reset generates new random credentials.":
      "Введите эти данные в HTTP-клиенте. «Сбросить» создаёт новые случайные.",
    "TUN DNS leak protection": "TUN: защита от утечек DNS",
    "TUN Kill-switch": "TUN: Kill-switch",
    "TUN Block LAN traffic": "TUN: блокировать LAN-трафик",
    "TUN FakeIP": "TUN FakeIP",
    "TUN Bootstrap DNS": "TUN Bootstrap DNS",
    "TUN Direct DNS": "TUN Direct DNS",
    "TUN Remote DNS": "TUN Remote DNS",
    "TUN Static hosts": "TUN: статические хосты",
    "Hard-coded answers checked before any DNS server. Useful for pinning the VPN server IP or working around broken DNS. Format: domain ip separated by spaces, one per line.":
      "Заданные вручную ответы, проверяются раньше любого DNS-сервера. Полезно, чтобы закрепить IP VPN-сервера или обойти нерабочий DNS. Формат: домен ip через пробел, по одной записи в строке.",
    "Minimize to tray on close": "Сворачивать в трей при закрытии",
    "Verbose logs": "Подробные логи",
    "Raises xray/sing-box log detail (level info) so the Logs panel shows per-connection lines. Takes effect on next connection.":
      "Повышает детализацию логов xray/sing-box (уровень info), чтобы в панели логов были видны строки по каждому соединению. Применяется при следующем подключении.",
    "Log speed/ping tests": "Логировать тесты скорости/пинга",
    "Save logs to a file": "Сохранять логи в файл",
    "Mirrors the Logs panel to a file that survives a restart or a crash — the in-app log is memory-only and starts empty every launch. Rotated at 5 MiB, one old copy kept (≈10 MiB max). Off by default: with verbose logs on, the file records which sites you visited, so share it only if you are fine with that.":
      "Дублирует панель логов в файл, который переживёт перезапуск или сбой — лог в программе хранится только в памяти и при каждом запуске пуст. Ротация на 5 МиБ, хранится одна старая копия (≈ 10 МиБ максимум). По умолчанию выключено: при включённых подробных логах в файл попадает, какие сайты вы открывали — делитесь им только если это вас устраивает.",
    "Logs each ping/speed result plus the full core output during the test (so you can see why a config is unavailable). Off by default — bulk tests can be noisy.":
      "Логирует каждый результат пинга/скорости и полный вывод ядра во время теста (видно, почему конфигурация недоступна). По умолчанию выключено — массовые тесты могут быть шумными.",
    "Logs": "Логи",
    "Copy": "Копировать",
    "Clear": "Очистить",
    "Settings font size (px)": "Размер текста в настройках (px)",
    "Language": "Язык",
    "Theme": "Тема",
    "Dark": "Тёмная",
    "Light": "Светлая",
    "close": "закрыть",
    "Data": "Данные",
    "Storage location": "Папка с данными",
    "Open folder": "Открыть",
    "Settings backup": "Резервная копия настроек",
    "Export": "Экспорт",
    "Import": "Импорт",
    "Exports tabs, tab settings and app settings to a JSON file. Import replaces the current state — useful when moving Vair to another computer.":
      "Экспортирует вкладки, настройки вкладок и приложения в JSON-файл. Импорт заменяет текущее состояние — удобно для переноса Vair на другой компьютер.",
    "Turn the toggle off to import only the app settings and keep your existing tabs.":
      "Выключите переключатель, чтобы импортировать только настройки приложения, оставив существующие вкладки.",
    "Import tabs and tab settings": "Импортировать вкладки и их настройки",

    // Placeholders
    "e.g. chrome.exe or firefox, press Enter": "напр. chrome.exe или firefox, нажмите Enter",

    // Small annotations
    "(resolves VPN server; plain UDP)": "(резолвит сервер VPN; обычный UDP)",
    "(for RU bypass / direct domains)": "(для RU-обхода и direct-доменов)",
    "(through proxy; DoH URL or IP)": "(через прокси; DoH URL или IP)",
    "(domain → IP; one per line)": "(домен → IP; по одному на строку)",

    "On a set interval: tabs with a source/URL reload the latest config list; pasted-only tabs (no source) just clear stale ping/speed results so they get re-tested.":
      "По заданному интервалу: вкладки с источником/URL подгружают свежий список конфигов; вкладки только со вставленными конфигами (без источника) просто сбрасывают устаревшие результаты ping/скорости, чтобы они были перепроверены.",

    // Hints (paragraphs)
    "Process names that bypass VPN. Only works in TUN mode (system proxy can't be excluded per-app at the OS level).":
      "Имена процессов, которые идут мимо VPN. Работает только в TUN-режиме (системный прокси нельзя исключить пер-приложение).",
    "Takes effect on next connection.": "Применится при следующем подключении.",
    "How many configs are pinged or speed-tested in parallel. Defaults: ping 10, speed 5. Takes effect on the next bulk test run.":
      "Сколько конфигов одновременно проверяется. По умолчанию: ping 10, speed 5. Действует при следующем массовом тесте.",
    "Warm-up timeout bounds the first un-measured request that establishes the tunnel (TCP + TLS/Reality handshake) — raise it if working configs are wrongly marked \"timeout\". Ping timeout is per round (3 rounds run, best is reported). Speed duration is how long the test downloads before computing throughput. Defaults: warm-up 4000 ms, ping 1500 ms, speed 4 s.":
      "Таймаут разогрева ограничивает первый незамеряемый запрос, который устанавливает туннель (TCP + TLS/Reality-рукопожатие) — увеличьте его, если рабочие конфиги ошибочно помечаются «timeout». Таймаут ping — на один раунд (всего 3 раунда, в результат идёт лучший). Длительность speed-теста — время скачивания, после которого считается скорость. По умолчанию: разогрев 4000 мс, ping 1500 мс, speed 4 с.",
    "Speed test runs for ~4 seconds regardless of file size, measuring throughput. Ping test accepts any HTTP response — pick whichever endpoint your provider routes best.":
      "Speed-тест работает заданное время (по умолчанию 4 с) независимо от размера файла. Ping принимает любой HTTP-ответ — выберите эндпоинт, который ваш провайдер маршрутизирует лучше.",
    "Default 9000 (jumbo frames). If you see download stalls or sites hanging, try 1500 or 1408. Takes effect on next connection.":
      "По умолчанию 9000 (jumbo). Если скачивания зависают или сайты не открываются — попробуйте 1500 или 1408. Применится при следующем подключении.",
    "Tracks bytes through the VPN tunnel in both modes. The lifetime total persists across sessions; the live session counter resets on every connect.":
      "Считает байты через VPN-туннель в обоих режимах. Итоговая сумма сохраняется между запусками; сессионный счётчик сбрасывается при каждом подключении.",
    "Forces all DNS queries through the tunnel using sing-box's built-in FakeIP. Without this, system DNS can escape through your ISP. Takes effect on next connection. Applies only to TUN mode.":
      "Принудительно отправляет все DNS-запросы через туннель, используя встроенный FakeIP в sing-box. Без этого DNS может утекать к провайдеру. Применится при следующем подключении. Работает только в TUN-режиме.",
    "Drops all traffic if the VPN drops — no fallback to your physical network (strict routing; on Windows a WFP filter). Also hardens DNS-leak protection. On by default; turn it off to let traffic fall back to a direct connection when the tunnel dies. Applies only to TUN mode.":
      "Сбрасывает весь трафик, если VPN упал — без возврата к физической сети (strict routing; на Windows — WFP-фильтр). Заодно усиливает защиту от утечек DNS. По умолчанию включено; выключите, чтобы при обрыве туннеля трафик возвращался к прямому соединению. Только для TUN-режима.",
    "By default 192.168.x.x and similar private addresses bypass the VPN so printers, NAS, and router admin pages still work. Enable this to force LAN traffic through the tunnel too — usually breaks local services.":
      "По умолчанию 192.168.x.x и подобные приватные адреса идут мимо VPN — это нужно, чтобы работали принтеры, NAS и админка роутера. Включите, чтобы LAN-трафик тоже шёл через туннель — обычно ломает локальные сервисы.",
    "FakeIP returns pseudo-addresses instantly and resolves the real domain inside the tunnel — fastest, no leak. Turn off to use a real DoH server through the proxy (slower but more compatible with apps that do their own DNS).":
      "FakeIP мгновенно отдаёт псевдо-адрес, а реальное имя резолвится уже внутри туннеля — быстро и без утечек. Отключите, чтобы использовать настоящий DoH через прокси (медленнее, но совместимо с приложениями, у которых свой DNS).",
    "Leave blank for defaults: Quad9 / Yandex / Cloudflare-over-IP. Pick servers that work on your ISP for bootstrap and direct; remote goes through the tunnel so anything reachable from your VPN server works.":
      "Оставьте пустым для значений по умолчанию: Quad9 / Yandex / Cloudflare-over-IP. Для bootstrap и direct выбирайте серверы, которые работают у вашего провайдера; remote идёт через туннель, поэтому подходит всё, что доступно с VPN-сервера.",
    "Increase or decrease the text size in the Settings and Tab settings modals only. The main window's typography is unchanged.":
      "Меняет размер текста только в окнах настроек программы и вкладок. Шрифты основного окна не меняются.",

    // openTabSettings
    "Tab Settings": "Настройки вкладки",
    // "SOURCES" stays as-is to match the untranslated section/tab name.
    "Sources Settings": "Настройки SOURCES",
    "Source URL (read-only)": "URL источника (только чтение)",
    "No source URL.": "Нет URL источника.",
    "Loading…": "Загрузка…",
    "copy": "копировать",
    "Show QR": "Показать QR",
    "QR code": "QR-код",
    "QR": "QR",
    "Name": "Имя",
    "Source URLs (raw links, base64 subscriptions)": "URL источников (raw-ссылки, base64-подписки)",
    "+ add URL": "+ добавить URL",
    "Files (loaded in addition order, after URLs)": "Файлы (грузятся в порядке добавления, после URL)",
    "+ add file": "+ добавить файл",
    "Files are read from disk on every RELOAD, so edits propagate without re-adding. No size limit — only the path is stored.":
      "Файлы читаются с диска при каждом RELOAD, так что правки подхватываются без повторного добавления. Без лимита на размер — хранится только путь.",
    "Auto-refresh interval (minutes, 0 = off)": "Интервал автообновления (минуты, 0 = выкл.)",
    "Test after auto-refresh": "Тест после авто-обновления",
    "After a scheduled auto-refresh (not a manual RELOAD), test the tab's configs in the background — ping only, or a full speed test.":
      "После планового авто-обновления (не ручного RELOAD) тестировать конфиги вкладки в фоне — только ping или полный тест скорости.",
    "off": "выкл.",
    "Ping only": "Только ping",
    "Speed test": "Тест скорости",
    "Subscription info": "Информация о подписке",
    "Subscription": "Подписка",
    "Failed to load": "Не удалось загрузить",
    "no configs found": "конфигурации не найдены",
    "Enable / disable this source": "Включить / отключить этот источник",
    "Test ping": "Тест пинга",
    "Test speed": "Тест скорости",
    "Traffic": "Трафик",
    "left": "осталось",
    "Expires": "Действует до",
    "days": "дн.",
    "Configs": "Конфигов",
    "Updated": "Обновлено",
    "Update interval": "Интервал обновления",
    "until": "до",
    "Deduplicate duplicate configs": "Удалять повторяющиеся конфиги",
    "Off": "Выкл.",
    "Hide": "Скрыть",
    "Delete": "Удалить",
    "No deduplication": "Без дедупликации",
    "Hide duplicates from view (reversible)": "Скрыть дубликаты из вида (обратимо)",
    "Permanently delete duplicates": "Безвозвратно удалить дубликаты",
    "Exclude filter": "Фильтр исключений",
    "Configs matching any of these are hidden. Type a value and press Enter to add it; matching is a case-insensitive substring. Leave a column empty to disable it.":
      "Конфиги, совпадающие с любым из значений, скрываются. Введите значение и нажмите Enter, чтобы добавить его; сравнение — подстрока без учёта регистра. Оставьте столбец пустым, чтобы отключить его.",
    "Off: show everything. Hide: filter from view, reversible. Delete: permanently remove duplicate entries. Matching is by vless body (ignores the name).":
      "Off: показывать всё. Hide: скрыть из вида (обратимо). Delete: безвозвратно удалить дубликаты. Сравнение по vless-телу (имя игнорируется).",

    // Running processes picker
    "Running processes": "Запущенные процессы",
    "filter…": "фильтр…",
    "Click a process to add it to the Apps without VPN list.": "Кликните по процессу, чтобы добавить его в список «Приложения без VPN».",
    "refresh": "обновить",
    "already added": "уже добавлен",
    "No process list available — only works in the desktop build.": "Список процессов недоступен — работает только в desktop-сборке.",
    "No matches": "Совпадений нет",

    // Tab settings — empty file list placeholder
    "No files added. Use the + add file button below.": "Файлы не добавлены. Используйте кнопку «+ добавить файл» ниже.",

    // Auto-connect panel
    "Auto-connect": "Авто-подключение",
    "Enable auto-connect": "Включить авто-подключение",
    "Connects to the fastest working config on launch and keeps it connected. While connected, the live link is monitored and, if it stops passing traffic, the app switches to another working config automatically.":
      "При запуске подключается к самому быстрому рабочему конфигу и поддерживает соединение. Пока подключено, следит за живым каналом и, если трафик перестаёт идти, автоматически переключается на другой рабочий конфиг.",
    "Candidate tabs": "Вкладки-кандидаты",
    "Which tabs' configs auto-connect may choose from. Defaults to Sources. Each tab's exclude filter is respected (hidden configs are never chosen), and each tab's own auto-refresh interval keeps its candidates up to date.":
      "Из каких вкладок авто-подключение выбирает конфиги. По умолчанию — SOURCES. Учитывается фильтр исключений каждой вкладки (скрытые конфиги не выбираются), а интервал авто-обновления каждой вкладки поддерживает её кандидатов в актуальном состоянии.",
    "Re-test candidates after refresh": "Перепроверять кандидатов после обновления",
    "After a candidate tab refreshes its config list, re-run ping on its configs so failover can rank them by real delay. Adds some test traffic on each refresh.":
      "После обновления списка конфигов на вкладке-кандидате заново измеряет их ping, чтобы переключение могло ранжировать их по реальной задержке. Добавляет немного тестового трафика при каждом обновлении.",
    "Connection mode": "Режим подключения",
    "Remember last": "Как в прошлый раз",
    "Health-check interval (s)": "Интервал проверки (с)",
    "Failure threshold": "Порог сбоев",
    "How often the live connection is probed, and how many checks in a row must fail before switching. Defaults: 15 s, 2.":
      "Как часто проверяется живое соединение и сколько проверок подряд должны провалиться до переключения. По умолчанию: 15 с, 2.",
    "Max latency (ms, 0 = off)": "Макс. задержка (мс, 0 = выкл)",
    "Treat the current config as failing when the live probe is slower than this, then switch to the fastest available config. 0 disables the speed check.":
      "Считать текущий конфиг сбойным, если живая проверка медленнее этого значения, и переключаться на самый быстрый доступный конфиг. 0 отключает проверку скорости.",
    "Status": "Состояние",
    "Auto-connect is off": "Авто-подключение выключено",
    "Idle — waiting": "Ожидание",
    "Paused — reconnect to resume": "Приостановлено — подключитесь, чтобы возобновить",
    "Connected": "Подключено",
    "Switching…": "Переключение…",
    "All candidates are down": "Все кандидаты недоступны",
    "Prefer faster (by speed test)": "Предпочитать быстрые (по тесту скорости)",
    "recommended": "рекомендуется",
    "Import from GitHub (private repo)": "Импорт из GitHub (приватный репозиторий)",
    "Pulls a config file from a private GitHub repository on every reload via a personal access token. Loaded after URLs and files.":
      "Загружает файл с конфигами из приватного репозитория GitHub при каждом обновлении, используя персональный токен доступа (PAT). Добавляется после URL и файлов.",
    "owner (user or organization)": "владелец (пользователь или организация)",
    "repository name": "название репозитория",
    "path to file, e.g. configs.txt": "путь к файлу, напр. configs.txt",
    "personal access token (PAT)": "персональный токен доступа (PAT)",
    "view": "показать",
    "hide": "скрыть",
    "Rank candidates by measured download speed (Mbps) instead of ping delay. Configs without a speed result fall back to ping order.":
      "Ранжировать кандидатов по измеренной скорости загрузки (Мбит/с), а не по задержке ping. Конфиги без результата скорости упорядочиваются по ping.",
    "Switch now": "Переключить сейчас",
    "Search settings…": "Поиск настроек…",
    "Remote access": "Удалённый доступ",
    "Web UI": "Веб-интерфейс",
    "Works only with this on:": "Работает только при включённой настройке:",

    "Tunnel": "Туннель",
    "Connection": "Соединение",
    "DPI bypass": "Обход DPI",

    "Local proxy": "Локальный прокси",
    "Default 19876. Change it if something else on this machine already listens on that port.": "По умолчанию 19876. Измените, если этот порт на этой машине уже занят.",

    "Proxy kill-switch": "Kill-switch для прокси",
    "Proxy mode only carries the applications that honour the system proxy — everything else has always gone straight out, connected or not. This blocks that at the firewall while a proxy session is up: what stays open is the engine and loopback, which is the proxy. Needs the same administrator rights TUN mode does, and releases on disconnect.": "Через proxy-режим идут только приложения, которые уважают системный прокси — всё остальное всегда ходило напрямую, подключены вы или нет. Эта настройка перекрывает такой трафик в брандмауэре на время сеанса: открытыми остаются движок и loopback, то есть сам прокси. Нужны те же права администратора, что и для TUN; снимается при отключении.",

    "TUN Keep blocking after the tunnel is gone": "TUN: продолжать блокировать после исчезновения туннеля",
    "The kill-switch above is enforced by the engine, so it ends when the engine does — a crash, or Vair being closed, returns the machine to its ordinary connection without saying so. This holds the block in the firewall instead: while it is engaged, nothing leaves except through the tunnel. It also covers the switch between two configurations, where the old adapter goes away and the ordinary routes come back for a second.": "Kill-switch выше держит движок, поэтому он заканчивается вместе с движком — падение или закрытие Vair молча возвращает машину на обычное соединение. Эта настройка держит блокировку в брандмауэре: пока она включена, наружу не уходит ничего, кроме как через туннель. Она же закрывает переключение между конфигурациями, где старый адаптер исчезает и на секунду возвращаются обычные маршруты.",
    "Read this: if Vair is killed while connected, this machine has no internet until you start Vair again (it releases the block at startup) or run in PowerShell as administrator:": "Прочитайте: если Vair будет убит во время подключения, на этой машине не будет интернета, пока вы не запустите Vair снова (при старте он снимает блокировку) или не выполните в PowerShell от администратора:",
    "Traffic is blocked at the firewall — the server stopped responding": "Трафик заблокирован брандмауэром — сервер перестал отвечать",

    "Watch the connected server": "Присматривать за подключённым сервером",
    "While connected, checks every few seconds that the server is still answering, and when it stops, closes the connection and sends a desktop notification instead of leaving \"connected\" on screen. Off by default: it ends a connection on the strength of a probe. Does not overlap with AUTO — while AUTO is on it does the watching itself and switches to another server rather than closing; this takes over only when AUTO is off.": "Пока есть подключение, каждые несколько секунд проверяет, отвечает ли сервер, и когда тот замолкает — закрывает соединение и присылает уведомление на рабочий стол, вместо того чтобы оставить на экране «подключено». По умолчанию выключено: соединение закрывается по результату одной проверки. С AUTO не пересекается — когда AUTO включён, он присматривает за узлом сам и переключается на другой сервер, а не закрывает соединение; эта настройка работает только при выключенном AUTO.",

    "Count rule matches": "Считать срабатывания правил",
    "Shows next to each rule below how many times it has fired, and marks the ones that never have — usually a typo. The engine has to report every routing decision for this, so its log becomes considerably more verbose. Takes effect on next connection.": "Показывает у каждого правила ниже, сколько раз оно сработало, и отмечает те, которые не сработали ни разу — обычно это опечатка. Для этого движок должен сообщать о каждом решении маршрутизации, поэтому его журнал становится заметно подробнее. Вступает в силу при следующем подключении.",
    "Last match": "Последнее срабатывание",
    "Never matched — check the spelling": "Ни разу не совпало — проверьте написание",
    "Not loaded yet — applies on the next connection": "Ещё не применено — вступит в силу при следующем подключении",
    "Matched inside the engine's own database — not counted here": "Совпадение ищется в собственной базе движка — здесь не считается",
    "Nothing has been routed yet": "Пока ничего не маршрутизировалось",
    "just now": "только что",
    "min ago": "мин назад",
    "h ago": "ч назад",
    "d ago": "дн назад",

    "Get a free WARP config": "Получить бесплатный WARP",
    "Getting WARP…": "Получаю WARP…",
    "WARP added": "WARP добавлен",

    "Prefer steadier (by measurement history)": "Предпочитать стабильные (по истории замеров)",
    "Rank on what a server has done over several measurements — its typical delay, charged for how much that delay varies and for probes it failed — instead of on the last measurement alone. A config that answers in 80 ms two tries out of three then ranks below one that answers in 100 ms every time, which is what you want for a call or a page load and not what you want for a leaderboard. Needs a few measurements per config before it changes anything; off by default because it changes which server AUTO picks.":
      "Ранжировать по тому, как сервер вёл себя за несколько замеров: по типичной задержке со штрафом за её разброс и за неудачные проверки, а не по одному последнему числу. Конфигурация, которая отвечает за 80 мс два раза из трёх, оказывается ниже той, что отвечает за 100 мс всегда, — именно это нужно для звонка или загрузки страницы и не нужно для таблицы рекордов. Начинает работать, когда по конфигурации накопится несколько замеров; по умолчанию выключено, потому что меняет выбор AUTO.",

    "Keep the system proxy set": "Удерживать системный прокси",
    "While connected, checks every 20 seconds that the system proxy setting still points at Vair and puts it back if something else changed it — another VPN client, a policy refresh, a browser extension. Without this, traffic leaves outside the tunnel while the app still says \"connected\", and nothing tells you. If it is overwritten again and again, Vair stops correcting it and says so in the log rather than fighting the other program. Turn off if something else is meant to own that setting.":
      "Пока есть подключение, раз в 20 секунд проверяет, что системный прокси всё ещё указывает на Vair, и возвращает настройку, если её изменило что-то другое — другой VPN-клиент, обновление политик, расширение браузера. Без этого трафик уходит мимо туннеля, а программа продолжает показывать «подключено», и узнать об этом неоткуда. Если настройку перебивают снова и снова, Vair перестаёт её возвращать и пишет об этом в лог, вместо того чтобы бороться с другой программой. Выключите, если этой настройкой должно управлять что-то другое.",

    "Show setting descriptions": "Показывать описания настроек",
    "The explanatory text under each setting, here and in the tab and auto-connect dialogs. Turn it off once the settings are familiar and the dialogs become much shorter.":
      "Пояснения под каждой настройкой — здесь, а также в настройках вкладки и авто-подключения. Выключите, когда настройки станут привычными: окна станут заметно короче.",
    "Runs a small web server on this PC so you can open the same interface in a browser on another device — a phone, a tablet, the computer next door. Access is protected by a secret key (in the link/QR below); anyone without it is refused. The connection is plain HTTP, NOT encrypted: everything, including the key, travels the local network in the clear, and whoever can read that traffic can control this program. Use it on a network you trust, and turn it off when you do not need it.":
      "Запускает на этом ПК небольшой веб-сервер, чтобы открыть тот же интерфейс в браузере другого устройства — телефона, планшета, соседнего компьютера. Доступ защищён секретным ключом (в ссылке/QR ниже); без него в доступе отказано. Соединение идёт по обычному HTTP и НЕ шифруется: всё, включая ключ, передаётся по локальной сети открытым текстом, и тот, кто её слушает, получает управление программой. Пользуйтесь только в сети, которой доверяете, и выключайте, когда не нужно.",
    "A source over plain http:// can be changed by anyone between you and the server, and what it returns becomes the servers your traffic goes through. Use https:// where the provider offers it.":
      "Источник по обычному http:// может подменить любой, кто находится между вами и сервером, — а то, что он вернёт, станет серверами, через которые пойдёт ваш трафик. Используйте https://, если провайдер его предлагает.",
    "Copied": "Скопировано",
    "Could not copy": "Не удалось скопировать",
    "Proxy access": "Доступ к прокси",
    "Localhost only": "Только этот ПК",
    "Custom": "Свой адрес",
    "Bind address": "Адрес привязки",
    "Address the proxy will use": "Адрес, который будет использоваться",
    "The address the local HTTP/SOCKS proxy listens on when connected. Localhost = this PC only. Allow LAN access = other devices on your network can reach it (binds 0.0.0.0). Custom = a specific interface address. Ports: HTTP 10819, SOCKS 10818 by default (above). Beyond localhost, use only on a trusted network — SOCKS keeps its password, HTTP has none. Applies on the next connection.":
      "Адрес, на котором слушает локальный HTTP/SOCKS-прокси при подключении. «Только этот ПК» — доступ лишь с этого компьютера. «Доступ из локальной сети» — прокси доступен другим устройствам в сети (привязка 0.0.0.0). «Свой адрес» — конкретный интерфейс. Порты: по умолчанию HTTP 10819, SOCKS 10818 (выше). За пределами localhost — только в доверенной сети: у SOCKS есть пароль, у HTTP нет. Применяется при следующем подключении.",
    "Set system proxy": "Задавать системный прокси",
    "When on (default), connecting in proxy mode points the system proxy setting at Vair, so every app that respects it goes through the VPN. Turn off for a \"non-system\" proxy: Vair only runs the local HTTP/SOCKS proxy on the ports above and changes nothing else — only apps where you explicitly set that proxy go through the VPN, the rest of the system stays direct. Applies immediately, even to a live connection.":
      "Когда включено (по умолчанию), при подключении в режиме proxy Vair прописывает себя системным прокси — через VPN идут все программы, которые его уважают. Выключите для «несистемного» прокси: Vair просто поднимет локальный HTTP/SOCKS-прокси на портах выше и больше ничего не тронет — через VPN пойдут только приложения, где вы сами укажете этот прокси, остальная система останется на прямом соединении. Применяется сразу, даже к активному подключению.",
    "On this desktop the setting is written to GNOME (gsettings). GNOME apps and browsers set to \"use system proxy settings\" follow it. Terminal tools — curl, git, apt, ssh — read the http_proxy environment variable instead and are NOT covered; point those at the proxy yourself.":
      "На этом рабочем столе настройка пишется в GNOME (gsettings). Её соблюдают приложения GNOME и браузеры, у которых выбрано «использовать системный прокси». Консольные инструменты — curl, git, apt, ssh — читают переменную окружения http_proxy и НЕ охвачены; их нужно направить на прокси самостоятельно.",
    "On this desktop the setting is written to KDE (kioslaverc). KDE apps and browsers set to \"use system proxy settings\" follow it. Terminal tools — curl, git, apt, ssh — read the http_proxy environment variable instead and are NOT covered; point those at the proxy yourself.":
      "На этом рабочем столе настройка пишется в KDE (kioslaverc). Её соблюдают приложения KDE и браузеры, у которых выбрано «использовать системный прокси». Консольные инструменты — curl, git, apt, ssh — читают переменную окружения http_proxy и НЕ охвачены; их нужно направить на прокси самостоятельно.",
    "This desktop has no system proxy setting Vair can drive, so this switch changes nothing here. Point applications at the local proxy yourself:":
      "На этом рабочем столе нет системной настройки прокси, которой Vair мог бы управлять, — переключатель здесь ничего не меняет. Направьте приложения на локальный прокси сами:",
    "TUN Share proxy over LAN": "TUN: раздавать прокси по локальной сети",
    "In TUN mode Vair normally routes only THIS PC through the tunnel. Turn this on to ALSO open the HTTP/SOCKS proxy (above) on the Proxy-access address, so another device (a phone) can route through this PC into the VPN while the PC itself stays fully tunnelled. Set Proxy access to \"Allow LAN access\" (or a custom address) for it to be reachable from other devices, and point the device at the HTTP port. Off by default; applies on the next connection.":
      "В TUN-режиме Vair обычно заворачивает в туннель только сам ПК. Включите это, чтобы ДОПОЛНИТЕЛЬНО открыть HTTP/SOCKS-прокси (выше) на адресе из «Доступ к прокси» — тогда другое устройство (телефон) сможет ходить в интернет через VPN этого ПК, при этом сам ПК остаётся полностью в туннеле. Чтобы прокси был доступен извне, поставьте «Доступ к прокси» в «Доступ из локальной сети» (или свой адрес) и укажите на устройстве HTTP-порт. По умолчанию выключено; применяется при следующем подключении.",
    "Add config…": "Добавить конфиг…",
    "This tab is empty": "Эта вкладка пуста",
    "Add a source": "Добавить источник",
    "Edit config…": "Изменить конфиг…",
    "View config…": "Просмотр конфига…",
    "Add configuration": "Добавить конфигурацию",
    "Edit configuration": "Изменить конфигурацию",
    "View configuration": "Просмотр конфигурации",
    "Basic": "Основное",
    "Transport": "Транспорт",
    "Protocol": "Протокол",
    "Address": "Адрес",
    "Port": "Порт",
    "Password": "Пароль",
    "Encryption method": "Метод шифрования",
    "Path": "Путь",
    "Host header": "Заголовок Host",
    "Service name": "Имя сервиса",
    "Header type": "Тип заголовка",
    "Fingerprint": "Отпечаток (fp)",
    "Allow insecure": "Разрешить небезопасное",
    "Public key": "Публичный ключ",
    "Encryption": "Шифрование",
    "Obfuscation": "Обфускация",
    "Obfs type": "Тип обфускации",
    "Obfs password": "Пароль обфускации",
    "Congestion control": "Контроль перегрузки",
    "UDP relay mode": "Режим UDP-relay",
    "Share link": "Ссылка (share link)",
    "(default)": "(по умолчанию)",
    "Add config": "Добавить",
    "Save": "Сохранить",
    "Config added": "Конфиг добавлен",
    "Config updated": "Конфиг обновлён",
    "Web UI port": "Порт веб-интерфейса",
    "IP address": "IP-адрес",
    "New key": "Новый ключ",
    "New key generated": "Создан новый ключ",
    "Generate a new key — old links and QR codes stop working":
      "Создать новый ключ — старые ссылки и QR-коды перестанут работать",
    "Reload": "Обновить",
    "Re-fetch the sources of the candidate tabs": "Обновить источники вкладок-кандидатов",
    "Detach": "Отделить",
    "Detach as a compact window": "Отделить в компактное окно",
    "Back to app": "Вернуться в приложение",
    "Show / hide settings": "Показать / скрыть настройки",
    "Click to connect to this config": "Нажмите, чтобы подключиться к этому конфигу",
    "Connect as chain": "Подключить цепочкой",
    "chain": "цепочка",
    "chain hop": "узел цепочки",
    "Rename": "Переименовать",
    "config": "конфиг",
    "configs": "конфигов",
    "Delete failed ping/speed": "Удалить с ошибкой ping/скорости",
    "Paste configs": "Вставить конфиги",
    "Select all": "Выделить все",
    "Scan QR from screen": "Сканировать QR с экрана",
    "Scan QR from file": "Сканировать QR из файла",
    "No QR code found": "QR-код не найден",
    "No QR code found on screen": "QR-код на экране не найден",
    "Could not read a QR from that image": "Не удалось прочитать QR из изображения",
    "QR is not a config or subscription": "QR не содержит конфиг или подписку",
    "Added from QR": "Добавлено из QR",
    "Subscription added to tab sources": "Подписка добавлена в источники вкладки",
    "Subscription already in tab sources": "Подписка уже есть в источниках вкладки",
    "Scanning screen…": "Сканирование экрана…",
    "Delete tab": "Удалить вкладку",
    "OK": "ОК",
    "Cancel": "Отмена",
    "check IP": "проверить IP",
    "checking…": "проверка…",
    "check failed": "ошибка проверки",
    "Exit IP as seen through the tunnel. Click to re-check.": "Выходной IP, видимый через туннель. Нажмите для повторной проверки.",
    "No eligible candidates yet — run ping on the candidate tabs.":
      "Подходящих кандидатов пока нет — запустите ping на вкладках-кандидатах.",
    "failed": "сбой",
    "untested": "не проверен",
    "previous config too slow": "предыдущий конфиг был слишком медленным",
    "previous config stopped responding": "предыдущий конфиг перестал отвечать",
    "slow, but the fastest available": "медленно, но это самый быстрый доступный",
    "manual switch": "ручное переключение",
    "candidate tabs changed": "вкладки-кандидаты изменились",
    "auto-connected": "авто-подключение",

    "cancel": "отмена",
    "save": "сохранить",
    "Loading configs…": "Загрузка конфигов…",
    "Deleting configs…": "Удаление конфигов…",
    "Changes apply automatically.": "Изменения применяются автоматически.",
    "Filter by name, host, type, transport or security. + combines conditions: germany+ws+tls needs all three (name, transport, security); germany+poland matches either (same column). Use field: to target a column, e.g. name:russia.": "Фильтр по name, host, type, transport или security. + объединяет условия: germany+ws+tls требует все три (имя, транспорт, защита); germany+poland — любое из (один столбец). Префикс поле: задаёт столбец, например name:russia."
}

export function t10(lang: string, en: string): string {
  return lang === 'ru' ? (RU[en] ?? en) : en
}
