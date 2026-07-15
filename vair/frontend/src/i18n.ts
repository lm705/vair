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
    "Local proxy ports apps connect to. Defaults: HTTP 10819, SOCKS 10818. Allow LAN access lets other devices on your network use the proxy (binds 0.0.0.0 instead of 127.0.0.1) — only on a trusted network; SOCKS keeps its password, HTTP has none. Takes effect on next connection.": "Локальные порты прокси, к которым подключаются приложения. По умолчанию: HTTP 10819, SOCKS 10818. «Доступ из локальной сети» разрешает другим устройствам в твоей сети пользоваться прокси (слушает 0.0.0.0 вместо 127.0.0.1) — только в доверенной сети; SOCKS защищён паролем, HTTP — нет. Применяется при следующем подключении.",
    "Network": "Сеть",
    "Statistics": "Статистика",
    "Security": "Безопасность",
    "DNS": "DNS",
    "System": "Система",
    "Appearance": "Внешний вид",

    // Settings — labels / buttons
    "Enable Sources tab": "Включить вкладку «SOURCES»",
    "Russian sites without VPN": "Российские сайты без VPN",
    "Routing mode": "Режим маршрутизации",
    "All traffic through VPN": "Весь трафик через VPN",
    "Everything except Russian sites": "Всё, кроме российских сайтов",
    "Only blocked-in-Russia resources": "Только заблокированное в РФ",
    "How traffic is split between the VPN and a direct connection. Takes effect on next connection.":
      "Как трафик распределяется между VPN и прямым подключением. Применяется при следующем подключении.",
    "Custom domains through VPN": "Свои домены через VPN",
    "e.g. youtube.com, press Enter": "напр. youtube.com, нажмите Enter",
    "Domains routed THROUGH the VPN in addition to the built-in blocked-list. Takes effect on next connection.":
      "Домены, которые идут ЧЕРЕЗ VPN в дополнение к встроенному списку блокировок. Применяется при следующем подключении.",
    "Custom blocklist URL": "Свой список блокировок (URL)",
    "Optional plain-text domain list (one per line) fetched and routed through the VPN. Auto-updated.":
      "Необязательный текстовый список доменов (по одному в строке): скачивается и идёт через VPN. Авто-обновляется.",
    "Custom domains without VPN": "Свои домены без VPN",
    "Apps without VPN (TUN mode only)": "Приложения без VPN (только TUN)",
    "Browse running processes": "Просмотреть запущенные процессы",
    "Ping concurrency": "Параллельных ping-тестов",
    "Speed concurrency": "Параллельных speed-тестов",
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
    "Launch at Windows startup": "Запускать при старте Windows",
    "Starts Vair automatically when you log in to Windows, minimized to the tray. Off by default.":
      "Автоматически запускает Vair при входе в Windows, свёрнутым в трей. По умолчанию выключено.",
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
    "Logs each ping/speed result plus the full core output during the test (so you can see why a config is unavailable). Off by default — bulk tests can be noisy.":
      "Логирует каждый результат пинга/скорости и полный вывод ядра во время теста (видно, почему конфигурация недоступна). По умолчанию выключено — массовые тесты могут быть шумными.",
    "Logs": "Логи",
    "Copy": "Копировать",
    "Clear": "Очистить",
    "Auto-scroll": "Автопрокрутка",
    "No logs yet — connect to a config to see core output.":
      "Логов пока нет — подключитесь к конфигу, чтобы увидеть вывод ядра.",
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
    "Replace current tabs and settings with the imported file? This cannot be undone.":
      "Заменить текущие вкладки и настройки данными из файла? Отменить это действие будет нельзя.",
    "Replace current app settings with the imported file? Tabs will not be touched.":
      "Заменить настройки приложения данными из файла? Вкладки затронуты не будут.",

    // Placeholders
    "e.g. vk.com, press Enter": "напр. vk.com, нажмите Enter",
    "e.g. chrome.exe, press Enter": "напр. chrome.exe, нажмите Enter",
    "e.g. Russia, press Enter": "напр. Russia, нажмите Enter",

    // Small annotations
    "(resolves VPN server; plain UDP)": "(резолвит сервер VPN; обычный UDP)",
    "(for RU bypass / direct domains)": "(для RU-обхода и direct-доменов)",
    "(through proxy; DoH URL or IP)": "(через прокси; DoH URL или IP)",
    "(domain → IP; one per line)": "(домен → IP; по одному на строку)",

    "On a set interval: tabs with a source/URL reload the latest config list; pasted-only tabs (no source) just clear stale ping/speed results so they get re-tested.":
      "По заданному интервалу: вкладки с источником/URL подгружают свежий список конфигов; вкладки только со вставленными конфигами (без источника) просто сбрасывают устаревшие результаты ping/скорости, чтобы они были перепроверены.",

    // Hints (paragraphs)
    "Route traffic to Russian domains and IPs directly, bypassing VPN. Takes effect on next connection.":
      "Трафик к российским доменам и IP идёт напрямую, минуя VPN. Применится при следующем подключении.",
    "Enter a domain — all its subdomains are included automatically. Takes effect on next connection.":
      "Введите домен — все его поддомены включаются автоматически. Применится при следующем подключении.",
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
    "Drops all traffic if the VPN goes down — no fallback to your physical network. Relies on the same strict-routing mechanism as DNS leak protection.":
      "Сбрасывает весь трафик, если VPN упал — без возврата к физической сети. Использует тот же механизм strict-routing, что и защита от утечек DNS.",
    "By default 192.168.x.x and similar private addresses bypass the VPN so printers, NAS, and router admin pages still work. Enable this to force LAN traffic through the tunnel too — usually breaks local services.":
      "По умолчанию 192.168.x.x и подобные приватные адреса идут мимо VPN — это нужно, чтобы работали принтеры, NAS и админка роутера. Включите, чтобы LAN-трафик тоже шёл через туннель — обычно ломает локальные сервисы.",
    "FakeIP returns pseudo-addresses instantly and resolves the real domain inside the tunnel — fastest, no leak. Turn off to use a real DoH server through the proxy (slower but more compatible with apps that do their own DNS).":
      "FakeIP мгновенно отдаёт псевдо-адрес, а реальное имя резолвится уже внутри туннеля — быстро и без утечек. Отключите, чтобы использовать настоящий DoH через прокси (медленнее, но совместимо с приложениями, у которых свой DNS).",
    "Leave blank for defaults: Quad9 / Yandex / Cloudflare-over-IP. Pick servers that work on your ISP for bootstrap and direct; remote goes through the tunnel so anything reachable from your VPN server works.":
      "Оставьте пустым для значений по умолчанию: Quad9 / Yandex / Cloudflare-over-IP. Для bootstrap и direct выбирайте серверы, которые работают у вашего провайдера; remote идёт через туннель, поэтому подходит всё, что доступно с VPN-сервера.",
    "Hard-coded answers checked before any DNS server. Useful for pinning the VPN server IP or working around broken DNS. Format: <code>domain ip</code> separated by spaces, one per line.":
      "Жёсткие ответы, проверяются до любого DNS-сервера. Удобно для прибивания IP VPN-сервера или обхода сломанного DNS. Формат: <code>домен ip</code> через пробел, по одному на строку.",
    "Increase or decrease the text size in the Settings and Tab settings modals only. The main window's typography is unchanged.":
      "Меняет размер текста только в окнах настроек программы и вкладок. Шрифты основного окна не меняются.",
    "Changes apply immediately when you reopen this dialog.":
      "Изменения применяются после повторного открытия диалога.",
    "Reset the lifetime traffic counter to 0? The current session is not affected.":
      "Сбросить общий счётчик трафика? Текущая сессия не затрагивается.",

    // openTabSettings
    "Tab Settings": "Настройки вкладки",
    // "SOURCES" stays as-is to match the untranslated section/tab name.
    "Sources Settings": "Настройки SOURCES",
    "Source URL (read-only)": "URL источника (только чтение)",
    "No source URL.": "Нет URL источника.",
    "Loading…": "Загрузка…",
    "Failed to load.": "Не удалось загрузить.",
    "copy": "копировать",
    "Show QR": "Показать QR",
    "QR code": "QR-код",
    "QR": "QR",
    "Scan with a proxy client on your phone.": "Отсканируйте прокси-клиентом на телефоне.",
    "Scan to open this source URL.": "Отсканируйте, чтобы открыть URL источника.",
    "Enter a URL first": "Сначала введите URL",
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
    "more, refine filter": "ещё, уточните фильтр",
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
    "Control from a phone on this network": "Управление с телефона в этой сети",
    "Runs a small web server on this PC so you can open the same interface in a phone — or another device — browser on the same Wi-Fi. Access is protected by a secret key (in the link/QR below); anyone without it is refused. Turn off when not needed.":
      "Запускает на этом ПК небольшой веб-сервер, чтобы открыть тот же интерфейс в браузере телефона — или другого устройства — в той же Wi-Fi. Доступ защищён секретным ключом (в ссылке/QR ниже); без него в доступе отказано. Выключайте, когда не нужно.",
    "Copied": "Скопировано",
    "Proxy access": "Доступ к прокси",
    "Localhost only": "Только этот ПК",
    "Custom": "Свой адрес",
    "Bind address": "Адрес привязки",
    "Address the proxy will use": "Адрес, который будет использоваться",
    "The address the local HTTP/SOCKS proxy listens on when connected. Localhost = this PC only. Allow LAN access = other devices on your network can reach it (binds 0.0.0.0). Custom = a specific interface address. Ports: HTTP 10819, SOCKS 10818 by default (above). Beyond localhost, use only on a trusted network — SOCKS keeps its password, HTTP has none. Applies on the next connection.":
      "Адрес, на котором слушает локальный HTTP/SOCKS-прокси при подключении. «Только этот ПК» — доступ лишь с этого компьютера. «Доступ из локальной сети» — прокси доступен другим устройствам в сети (привязка 0.0.0.0). «Свой адрес» — конкретный интерфейс. Порты: по умолчанию HTTP 10819, SOCKS 10818 (выше). За пределами localhost — только в доверенной сети: у SOCKS есть пароль, у HTTP нет. Применяется при следующем подключении.",
    "Server port": "Порт сервера",
    "IP address": "IP-адрес",
    "Default 19876. Change it if that port is taken — e.g. while the 1.10 release is still running on this PC.":
      "По умолчанию 19876. Смените, если порт занят — например, пока на этом ПК ещё работает версия 1.10.",
    "New key": "Новый ключ",
    "New key generated": "Создан новый ключ",
    "Generate a new key — old links and QR codes stop working":
      "Создать новый ключ — старые ссылки и QR-коды перестанут работать",
    "Scan the QR from your phone camera. If this address is unreachable, try another IP: ":
      "Отсканируйте QR камерой телефона. Если адрес недоступен, попробуйте другой IP: ",
    "Reload": "Обновить",
    "Re-fetch the sources of the candidate tabs": "Обновить источники вкладок-кандидатов",
    "Detach": "Отделить",
    "Detach as a compact window": "Отделить в компактное окно",
    "Back to app": "Вернуться в приложение",
    "Show / hide settings": "Показать / скрыть настройки",
    "Click to connect to this config": "Нажмите, чтобы подключиться к этому конфигу",
    "Could not connect to that config": "Не удалось подключиться к этому конфигу",
    "Connect as chain": "Подключить цепочкой",
    "chain": "цепочка",
    "chain hop": "узел цепочки",
    "Select at least 2 configs to chain": "Выберите минимум 2 конфига для цепочки",
    "Chain failed": "Не удалось подключить цепочку",
    "Rename": "Переименовать",
    "Rename config": "Переименовать конфиг",
    "New config name:": "Новое имя конфига:",
    "Rename failed": "Не удалось переименовать",
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
    "No configs found in the QR": "В QR не найдено конфигов",
    "Could not import from QR": "Не удалось импортировать из QR",
    "Subscription added to tab sources": "Подписка добавлена в источники вкладки",
    "Subscription already in tab sources": "Подписка уже есть в источниках вкладки",
    "Could not add subscription": "Не удалось добавить подписку",
    "Scanning screen…": "Сканирование экрана…",
    "Not supported here": "Недоступно здесь",
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
    "Enable auto-connect first": "Сначала включите авто-подключение",
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
