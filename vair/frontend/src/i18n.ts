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
    "Local sites of these countries often refuse foreign/VPN addresses (banks, government services) — routing them direct keeps them working while everything else goes through the VPN. Each country is matched by its TLD zone (*.ru, *.cn, *.ir, *.kz), its known-domains list and its IP ranges. With nothing selected, everything goes through the VPN. Takes effect on the next connection.":
      "Локальные сайты этих стран часто не пускают с зарубежных/VPN-адресов (банки, госуслуги) — прямой маршрут оставляет их доступными, а весь остальной трафик идёт через VPN. Страна определяется по её доменной зоне (*.ru, *.cn, *.ir, *.kz), списку известных доменов и IP-диапазонам. Если ничего не выбрано — весь трафик идёт через VPN. Применяется при следующем подключении.",
    "Only blocked-in-China resources": "Только заблокированное в Китае",
    "Custom rules — without VPN": "Свои правила — без VPN",
    "Custom rules — through VPN": "Свои правила — через VPN",
    "Custom rules — through VPN (URL)": "Свои правила — через VPN (по ссылке)",
    "https://…/domains.txt — press Enter": "https://…/domains.txt — Enter для добавления",
    "A URL-backed version of the list above: one or more links to plain-text domain lists (one domain per line), fetched, auto-updated and routed THROUGH the VPN. Handy for large or community-maintained lists instead of typing each domain. Press Enter to add a link. Takes effect on the next connection.":
      "Версия списка выше по ссылке: одна или несколько ссылок на текстовые списки доменов (по одному в строке), которые скачиваются, авто-обновляются и идут ЧЕРЕЗ VPN. Удобно для больших или поддерживаемых сообществом списков — вместо ввода каждого домена вручную. Enter — добавить ссылку. Применяется при следующем подключении.",
    "Custom rules — block": "Свои правила — блокировать",
    "domain, IP or CIDR — press Enter": "домен, IP или CIDR — Enter",
    "Kept OFF the VPN (direct). The list applies in both modes. Accepts a domain (all subdomains included), an IP or a CIDR (e.g. 10.0.0.0/8), or an advanced prefix — full:, regexp:, domain:. Two prefixes are the exception: geosite: and geoip: name categories inside xray’s own databases, so they apply in proxy mode and are ignored in TUN. Takes effect on the next connection.":
      "Идут мимо VPN (напрямую). Список действует в обоих режимах. Принимается домен (все поддомены включаются), IP или CIDR (например, 10.0.0.0/8), либо продвинутый префикс — full:, regexp:, domain:. Исключение — два префикса: geosite: и geoip: называют категории из собственных баз xray, поэтому действуют в режиме proxy и игнорируются в TUN. Применяется при следующем подключении.",
    "Forced THROUGH the VPN — even in a bypass mode. Same syntax as above. In whitelist mode (Only selected sites) this is the main list. Takes effect on the next connection.":
      "Принудительно идут ЧЕРЕЗ VPN — даже в режимах-исключениях. Синтаксис как выше. В режиме белого списка («Только выбранные сайты») это основной список. Применяется при следующем подключении.",
    "Dropped entirely (ads, telemetry, unwanted hosts). Same syntax as above. Highest priority: block wins over the without-VPN and through-VPN lists. Takes effect on the next connection.":
      "Полностью блокируются (реклама, телеметрия, нежелательные хосты). Синтаксис как выше. Наивысший приоритет: блокировка важнее списков «без VPN» и «через VPN». Применяется при следующем подключении.",
    "Regular traffic goes DIRECT; only resources blocked in China go through the VPN — matched by the community GFW list, which updates itself, plus your own lists below.":
      "Обычный трафик идёт НАПРЯМУЮ; через VPN — только ресурсы, заблокированные в Китае: по общедоступному списку GFW, который обновляется сам, плюс ваши списки ниже.",
    "Whitelist mode: ONLY the domains you list below go through the VPN — everything else stays on the direct connection. With an empty list, nothing is tunnelled. If \"Set system proxy\" is off, the app filter also applies (only apps pointed at the proxy, AND only these domains).":
      "Режим белого списка: через VPN идут ТОЛЬКО перечисленные ниже домены — всё остальное остаётся на прямом соединении. При пустом списке через туннель не идёт ничего. Если «Задавать системный прокси» выключено, добавляется и фильтр по приложениям (только приложения, указавшие прокси, И только эти домены).",
    "How traffic is split between the VPN and a direct connection. Takes effect on the next connection.":
      "Как трафик распределяется между VPN и прямым подключением. Применяется при следующем подключении.",
    "Custom domains without VPN": "Свои домены без VPN",
    "TUN Apps without VPN": "TUN: приложения без VPN",
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
    "TUN MTU": "TUN MTU",
    "TLS fragmentation (DPI bypass)": "Фрагментация TLS (обход DPI)",
    "TLS fingerprint (uTLS)": "Отпечаток TLS (uTLS)",
    "Which browser's TLS handshake xray imitates (uTLS) when a config has no fp= of its own — without it the connection uses an easily-fingerprinted TLS signature. Applies to TLS/Reality nodes. Default: chrome. Takes effect on the next connection.":
      "Под какой браузер xray маскирует TLS-рукопожатие (uTLS), если в конфиге нет своего fp= — без этого у соединения легко распознаваемый TLS-отпечаток. Действует для узлов TLS/Reality. По умолчанию: chrome. Применяется при следующем подключении.",
    "Splits the TLS handshake (ClientHello) into pieces so a DPI can't match the connection in a single packet. Helps when the server is alive but the handshake is being reset. xray protocols only (VLESS/VMess/Trojan/SS over TLS). Takes effect on the next connection.":
      "Разбивает рукопожатие TLS (ClientHello) на части, чтобы DPI не мог распознать соединение в одном пакете. Помогает, когда сервер живой, но рукопожатие сбрасывается. Только протоколы xray (VLESS/VMess/Trojan/SS поверх TLS). Применяется при следующем подключении.",
    "Fragment length": "Длина фрагмента",
    "Fragment interval (ms)": "Интервал фрагментации (мс)",
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
    "This copy is installed in a folder Vair cannot write to, so it updates by running the setup program, which will ask for administrator rights.":
      "Эта копия установлена в папку, куда Vair не может писать, поэтому обновляется через установщик — он запросит права администратора.",
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
    "Last speed measurements": "Последние замеры скорости",
    "The last": "Последние",
    "speed measurements, oldest first. A speed test is expensive, so these are rare and far apart — the ranking only uses ones under":
      "замеров скорости, сверху самые старые. Тест скорости дорогой, поэтому замеров мало и они редки — ранжирование берёт только те, что моложе",
    "hours old.": "часов.",
    "time not kept": "время не сохранено",
    "In milliseconds, oldest first — the last": "В миллисекундах, сверху самые старые — последние",
    "checks, as they came, with when each round was taken. The figures above are worked out differently: every check folds into a running average at":
      "проверок, как они были, и когда снят каждый круг. Числа выше считаются иначе: каждая проверка входит в скользящее среднее с весом",
    "so about": "поэтому их определяют примерно",
    "recent ones decide them — which is why the two need not agree.":
      "последних — поэтому со списком они совпадать не обязаны.",
    "History weight": "Вес истории",
    "nothing is dropped — each older check simply counts for less, so a server that was bad yesterday is forgiven within a few runs":
      "ничего не выбрасывается — каждая следующая проверка весит меньше, поэтому вчерашняя неудача прощается за несколько прогонов",
    "typical speed less its spread — what the server can be relied on to deliver":
      "типичная скорость за вычетом разброса — то, на что на этом сервере можно рассчитывать",
    "spread": "разброс",
    "delay checks": "проверок задержки",
    "needs": "нужно",
    "checks to judge": "проверок для оценки",
    "Last delay checks": "Последние проверки задержки",
    "Delay checks": "Проверок задержки",
    "a speed test makes one of these too — it measures the delay before the download":
      "тест скорости добавляет сюда одну: перед загрузкой он меряет задержку",
    "Last delay check": "Последняя проверка задержки",
    "Ranked on": "В ранжировании",
    "typical delay plus its spread and a charge for unanswered checks — this is what the steady modes compare":
      "типичная задержка плюс разброс и штраф за неотвеченные проверки — именно это сравнивают режимы «устойчиво»",
    "Speed measurements": "Замеров скорости",
    "none": "нет",
    "the steady mode wants": "режиму «устойчиво» нужно",
    "of them; below that it ranks on the last one": "замеров; пока их меньше, ранжирование идёт по последнему",
    "Last speed measurement": "Последний замер скорости",
    "older than": "старше",
    "hours, so the ranking no longer uses it": "часов, поэтому ранжирование его больше не берёт",
    "jitter": "разброс",
    "checks": "проверок",
    "never measured": "не измерялась",
    "too old to rank on": "устарела для ранжирования",
    "Detail": "Подробности",
    "the whole history; the averages lean on the last few":
      "за всю историю; средние опираются на последние несколько",
    "Speed spread": "Разброс скорости",
    "a speed figure counts for": "замер скорости учитывается",
    "Oldest first — the last twelve checks, in milliseconds. The figures above are running averages that lean on the newest results, so they do not always match this list.":
      "Слева — самые старые; последние двенадцать проверок, в миллисекундах. Числа выше — скользящие средние с перевесом в сторону свежих результатов, поэтому со списком они совпадают не всегда.",
    // AUTO panel — why a candidate sits where it does
    "history": "история",
    "paused": "пауза",
    "Ranked on its measurement history": "Место в списке — по истории замеров",
    "Skipped after a failure — auto-connect will try it again in":
      "Пропускается после неудачи — авто-подключение вернётся к нему через",
    // Connection log (the AUTO panel)
    "Connection log": "Журнал подключений",
    "Show / hide the connection log": "Показать / скрыть журнал подключений",
    "The last hundred connections and what ended each one. Kept on this computer; nothing about the traffic itself is recorded.":
      "Последняя сотня подключений и то, чем закончилось каждое. Хранится на этом компьютере; о самом трафике не записывается ничего.",
    "No connections recorded yet": "Подключений пока не было",
    // Measurement history (the Stability cell / row menu)
    "Measurement history": "История замеров",
    "Measurement history…": "История замеров…",
    "This configuration": "Эта конфигурация",
    "This configuration is no longer in the tab.": "Этой конфигурации больше нет на вкладке.",
    "Nothing has been measured yet. Run PING on the row — the history starts with the first check.":
      "Замеров ещё не было. Нажмите PING в строке — история начнётся с первой проверки.",
    "Delay": "Задержка",
    "Typical": "Обычно",
    "Last check": "Последняя проверка",
    "Last checks": "Последние проверки",
    "Oldest first. Kept for the last twelve checks; the numbers above cover the whole history.":
      "Слева — самые старые. Хранятся последние двенадцать проверок; числа выше — по всей истории.",
    "Speed": "Скорость",
    "Spread": "Разброс",
    "Ranked as": "В ранжировании",
    "typical less its spread — what the server can be relied on to deliver":
      "обычная скорость минус разброс — то, на что на этом сервере можно рассчитывать",
    "Measurements": "Замеров",
    "Measured": "Измерена",
    "never": "ни разу",
    "run SPEED on the row to measure it": "нажмите SPEED в строке, чтобы измерить",
    "too old to rank on — a speed figure counts for": "слишком старая для ранжирования — замер скорости учитывается",
    "hours": "часов",
    "the running mean, weighted towards the newest checks": "скользящее среднее с перевесом в сторону свежих проверок",
    "The history belongs to the server, not to the row: it survives a list refresh, and a configuration that appears in two tabs has one history. It is forgotten after":
      "История принадлежит серверу, а не строке: она переживает обновление списка, и у конфигурации, попавшей на две вкладки, история одна. Забывается через",
    "days without a check.": "дней без единой проверки.",
    "days ago": "дн назад",
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
    "Protects the local SOCKS5 proxy (proxy mode) with a username/password so other local apps can't use it or probe your VPN server. Off by default; turn it on to require credentials. Takes effect on the next connection.":
      "Защищает локальный SOCKS5-прокси (режим proxy) логином/паролем, чтобы другие локальные приложения не могли им воспользоваться или определить ваш VPN-сервер. По умолчанию выключено; включите, чтобы требовать логин/пароль. Применяется при следующем подключении.",
    "SOCKS username": "Логин SOCKS",
    "SOCKS password": "Пароль SOCKS",
    "Generate new credentials": "Сгенерировать новые данные",
    "Reset": "Сбросить",
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
    "Protects the local HTTP proxy with a username/password (Basic auth). Applies both to proxy mode and to the TUN “Share proxy over LAN” HTTP proxy. Off by default. Takes effect on the next connection.":
      "Защищает локальный HTTP-прокси логином/паролем (Basic-авторизация). Действует и в режиме proxy, и для HTTP-прокси функции «Раздавать прокси по LAN в TUN-режиме». По умолчанию выключено. Применяется при следующем подключении.",
    "HTTP username": "Логин HTTP",
    "HTTP password": "Пароль HTTP",
    "TUN DNS leak protection": "TUN: защита от утечек DNS",
    "TUN Kill-switch": "TUN: Kill-switch",
    "TUN Block LAN traffic": "TUN: блокировать LAN-трафик",
    "TUN FakeIP": "TUN FakeIP",
    "TUN Bootstrap DNS": "TUN Bootstrap DNS",
    "TUN Direct DNS": "TUN Direct DNS",
    "TUN Remote DNS": "TUN Remote DNS",
    "TUN Static hosts": "TUN: статические хосты",
    "Answers you write yourself, checked before any DNS server is asked. One per line: the name, a space, the address — example.com 93.184.216.34. Useful for pinning the address of the VPN server itself, or for getting past a name that resolves to the wrong thing.":
      "Ответы, записанные вами: проверяются раньше, чем спрашивается любой DNS-сервер. По одному в строке — имя, пробел, адрес: example.com 93.184.216.34. Пригодится, чтобы закрепить адрес самого VPN-сервера или обойти имя, которое резолвится не туда.",
    "Minimize to tray on close": "Сворачивать в трей при закрытии",
    "Verbose logs": "Подробные логи",
    "Raises xray/sing-box log detail (level info) so the Logs panel shows per-connection lines. Takes effect on the next connection.":
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
    "Import tabs and tab settings": "Импортировать вкладки и их настройки",

    // Placeholders
    "e.g. Discord.exe or firefox, press Enter": "напр. Discord.exe или firefox, нажмите Enter",

    // Small annotations
    "(resolves VPN server; plain UDP)": "(резолвит сервер VPN; обычный UDP)",
    "(for RU bypass / direct domains)": "(для RU-обхода и direct-доменов)",
    "(through proxy; DoH URL or IP)": "(через прокси; DoH URL или IP)",
    "(domain → IP; one per line)": "(домен → IP; по одному на строку)",

    "On a set interval: tabs with a source/URL reload the latest config list; pasted-only tabs (no source) just clear stale ping/speed results so they get re-tested.":
      "По заданному интервалу: вкладки с источником/URL подгружают свежий список конфигов; вкладки только со вставленными конфигами (без источника) просто сбрасывают устаревшие результаты ping/скорости, чтобы они были перепроверены.",

    // Hints (paragraphs)
    "Process names that bypass VPN; upper and lower case are the same. A full path picks out one of two programs sharing a name. Only works in TUN mode (system proxy can't be excluded per-app at the OS level).":
      "Имена процессов, которые идут мимо VPN; регистр не важен. Полный путь позволяет выбрать одну из двух программ с одинаковым именем. Работает только в TUN-режиме (системный прокси нельзя исключить пер-приложение).",
    "Takes effect on the next connection.": "Применяется при следующем подключении.",
    "Default 9000 (jumbo frames). If you see download stalls or sites hanging, try 1500 or 1408. Takes effect on the next connection.":
      "По умолчанию 9000 (jumbo). Если скачивания зависают или сайты не открываются — попробуйте 1500 или 1408. Применяется при следующем подключении.",
    "Forces all DNS queries through the tunnel using sing-box's built-in FakeIP. Without this, system DNS can escape through your ISP. Takes effect on the next connection. Applies only to TUN mode.":
      "Принудительно отправляет все DNS-запросы через туннель, используя встроенный FakeIP в sing-box. Без этого DNS может утекать к провайдеру. Применяется при следующем подключении. Работает только в TUN-режиме.",
    "Drops all traffic if the VPN drops — no fallback to your physical network (strict routing; on Windows a WFP filter). Also hardens DNS-leak protection. On by default; turn it off to let traffic fall back to a direct connection when the tunnel dies. Applies only to TUN mode.":
      "Сбрасывает весь трафик, если VPN упал — без возврата к физической сети (strict routing; на Windows — WFP-фильтр). Заодно усиливает защиту от утечек DNS. По умолчанию включено; выключите, чтобы при обрыве туннеля трафик возвращался к прямому соединению. Только для TUN-режима.",
    "By default 192.168.x.x and similar private addresses bypass the VPN so printers, NAS, and router admin pages still work. Enable this to force LAN traffic through the tunnel too — usually breaks local services.":
      "По умолчанию 192.168.x.x и подобные приватные адреса идут мимо VPN — это нужно, чтобы работали принтеры, NAS и админка роутера. Включите, чтобы LAN-трафик тоже шёл через туннель — обычно ломает локальные сервисы.",
    "What a program gets back when it looks up a name in TUN mode. On (default): Vair answers at once with a stand-in address and looks the name up for real inside the tunnel, at the moment the connection is made. Nothing is asked over the network before the tunnel exists, so nothing can leak and nothing waits on a lookup. Off: names are resolved through the tunnel the ordinary way, before the connection starts — slower, and the right answer for a program that does its own DNS and expects a real address back.":
      "Что получает программа, спросив адрес по имени в режиме TUN. Включено (по умолчанию): Vair сразу отвечает подставным адресом, а по-настоящему ищет имя уже внутри туннеля, в момент подключения. До появления туннеля в сеть не уходит ни одного запроса — утекать нечему, и ждать ответа никому не приходится. Выключено: имена разрешаются через туннель обычным порядком, до начала соединения, — медленнее, но это правильный ответ для программы, которая делает DNS сама и ждёт настоящий адрес.",
    "The text size in this window and the other dialogs — tab settings, AUTO, connections, the service check. Between 9 and 20 px; default 12.":
      "Размер текста в этом окне и в остальных диалогах — настройках вкладки, AUTO, соединениях, проверке сервисов. От 9 до 20 px, по умолчанию 12.",
    "Interface font size (px)": "Размер текста интерфейса (px)",
    "The text size in the main window — toolbar, tabs, the table, the connection bar and the log panel. Between 10 and 18 px; default 13. A narrower range than the dialogs have, because the table's columns are a fixed width and its rows a fixed height: past a point the text stops fitting in them rather than the window growing.":
      "Размер текста в основном окне — панель инструментов, вкладки, таблица, строка подключения и панель логов. От 10 до 18 px, по умолчанию 13. Диапазон уже, чем у диалогов: у столбцов таблицы фиксированная ширина, а у строк — высота, поэтому с какого-то момента текст перестаёт в них помещаться, вместо того чтобы окно росло.",

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
    "Off shows everything, Hide filters duplicates out of the view (reversible), Delete removes them for good. Two configurations count as duplicates when they point at the same server with the same settings; the name is not compared.":
      "«Выкл.» показывает всё, «Скрыть» убирает дубликаты из вида (обратимо), «Удалить» стирает их насовсем. Дубликатами считаются конфигурации, ведущие на один и тот же сервер с одинаковыми настройками; имя при сравнении не учитывается.",

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
    "After a candidate tab refreshes its config list, re-test its configs so failover has something real to rank them by — ping, and the speed test as well when the ranking uses speed. Adds test traffic on each refresh, and more of it in a speed mode.": "После обновления списка конфигураций во вкладке-кандидате перепроверять её конфигурации, чтобы переключению было на что опираться: ping, а в скоростных режимах ранжирования — ещё и speed-тест. Добавляет тестовый трафик при каждом обновлении, в скоростном режиме заметно больше.",
    "Rank candidates by": "Ранжировать кандидатов",
    "Delay (last ping)": "По задержке (последний ping)",
    "Speed (last test)": "По скорости (последний тест)",
    "Delay, steady (history)": "По задержке, устойчиво (история)",
    "Speed, steady (history)": "По скорости, устойчиво (история)",
    "The last measured ping. Cheap and quick to react, and as noisy as one measurement taken under load.": "Последний измеренный ping. Дёшево и быстро реагирует — и настолько же шумно, насколько шумен один замер под нагрузкой.",
    "The last measured download speed. Configs with no speed result fall back to ping order.": "Последняя измеренная скорость скачивания. Конфигурации без результата speed-теста упорядочиваются по задержке.",
    "Typical delay over several measurements, charged for how much it varies and for probes that failed. A server answering in 80 ms two tries out of three ranks below one answering in 100 ms every time — right for a call or a page load, wrong for a leaderboard. Needs a few measurements per config.": "Типичная задержка за несколько замеров, со штрафом за разброс и за неудачные проверки. Сервер, отвечающий за 80 мс два раза из трёх, окажется ниже того, который отвечает за 100 мс всегда, — это верно для звонка или загрузки страницы и неверно для таблицы рекордов. Нужно несколько замеров на конфигурацию.",
    "The speed a server can be relied on: its typical speed less how much that varies, so steady beats bursty at the same average. A failed speed test is not counted against the server — that is usually the test endpoint refusing. Needs a few speed tests per config, and forgets them after six hours, because a speed measured this morning describes this morning.": "Скорость, на которую можно рассчитывать: типичная скорость за вычетом разброса, поэтому при равном среднем ровный сервер выигрывает у того, что выдаёт рывками. Неудачный speed-тест серверу не засчитывается — обычно это отказ тестовой площадки. Нужно несколько тестов на конфигурацию, и они забываются через шесть часов: скорость, измеренная утром, описывает утро.",

    "Both are on, and speed decides: history only separates configs whose speed is equal or unmeasured. Turn \"prefer faster\" off to rank by history.": "Включены оба, и решает скорость: история разделяет только конфигурации с равной или неизмеренной скоростью. Чтобы ранжировать по истории, выключите «предпочитать быстрые».",
    "This transport is xray-only: a configuration using it connects through xray in both modes, and cannot be an intermediate hop of a sing-box chain.": "Этот транспорт умеет только xray: конфигурация с ним подключается через xray в обоих режимах и не может быть промежуточным узлом цепочки на sing-box.",
    "xhttp mode": "Режим xhttp",
    "mKCP header": "Маскировка mKCP",
    "mKCP seed": "Ключ mKCP",
    "Private key": "Приватный ключ",
    "Peer's public key": "Публичный ключ пира",
    "Pre-shared key": "Общий ключ (PSK)",
    "Interface address": "Адрес интерфейса",
    "MTU": "MTU",
    "Reserved": "Reserved",
    "WireGuard has no handshake to fail politely: a wrong key, a missing interface address or the wrong reserved bytes all look the same from here — the tunnel comes up and nothing crosses it.": "WireGuard не умеет вежливо сообщать об ошибке: неверный ключ, отсутствующий адрес интерфейса и неправильные reserved-байты выглядят одинаково — туннель поднимается, и через него ничего не идёт.",

    "Record connections": "Записывать соединения",
    "Off stops all collection: no connection is recorded and nothing is read to find out where it is going. What is already here stays until you clear it.": "Выключение полностью прекращает сбор: соединения не записываются и ничего не читается, чтобы узнать их назначение. Уже собранное остаётся, пока не нажать «очистить».",
    "Bandwidth and ports": "Полоса и порты",
    "User name": "Имя пользователя",
    "gRPC mode": "Режим gRPC",
    "SpiderX": "SpiderX",
    "Plugin": "Плагин",
    "Upload (Mbps)": "Отдача (Мбит/с)",
    "Download (Mbps)": "Приём (Мбит/с)",
    "Port hopping": "Перебор портов",
    "Certificate pin": "Отпечаток сертификата",
    "A plain proxy, with no encryption of its own: TLS here only wraps the connection to the proxy itself. Everything it carries is as protected as the protocol inside it.": "Обычный прокси, без собственного шифрования: TLS здесь оборачивает только соединение с самим прокси. Всё, что он передаёт, защищено ровно настолько, насколько защищён протокол внутри.",

    "Not through Vair": "Мимо Vair",
    "Nothing outside the proxy": "Мимо прокси ничего нет",
    "State": "Состояние",
    "recording": "запись",
    "The machine's own connections that did not go through the proxy — programs that do not use it": "Соединения самой машины, которые не прошли через прокси, — программы, которые им не пользуются",
    "Off stops all collection: no connection is recorded and nothing is read to find out where it is going. What is already here stays.": "Выключение полностью прекращает сбор: соединения не записываются и ничего не читается, чтобы узнать их назначение. Уже собранное остаётся.",
    "established": "установлено",
    "connecting": "подключается",
    "closing": "закрывается",

    "Check IP service": "Сервис проверки IP",
    "Custom check IP URL": "Свой URL проверки IP",
    "Delete duplicates of this one": "Удалить дубликаты этой конфигурации",
    "Duplicates removed": "Удалено дубликатов",
    "No duplicates of this one": "Дубликатов этой конфигурации нет",
    "Where the check IP button asks who it appears to be. Any service answering JSON works; in the bypass routing modes this one host is forced through the tunnel, or the answer would describe the connection the VPN is not using.": "Где кнопка проверки IP спрашивает, каким вас видят. Подойдёт любой сервис, отвечающий JSON; в режимах-исключениях этот хост принудительно идёт через туннель, иначе ответ описывал бы соединение, которым VPN не пользуется.",

    "Since the program started": "С момента запуска программы",
    "search program, destination…": "поиск: программа, назначение…",
    "search configuration…": "поиск по конфигурации…",
    "edit list": "изменить список",
    "done": "готово",
    "Address to ask": "Адрес для запроса",
    "Open if the answer has": "Открыт, если в ответе есть",
    "Refused if the answer has": "Отказ, если в ответе есть",
    "Remove": "Удалить",
    "My bank": "Мой банк",
    "(any answer)": "(любой ответ)",
    "(only 403)": "(только 403)",
    "add service": "добавить сервис",
    "restore defaults": "вернуть стандартные",
    "Filter the table by an answer: unlock:chatgpt": "Фильтр таблицы по ответу: unlock:chatgpt",
    "A service is asked once per configuration. Anything that answers 403, or whose answer contains one of the refusal markers, counts as refused; with an \"open if\" marker set, only an answer containing it counts as open.": "Каждый сервис опрашивается один раз на конфигурацию. Ответ 403 или содержащий любую из строк-признаков отказа считается отказом; если задана строка «открыт, если», открытым считается только ответ с ней.",

    "Service check": "Проверка доступа",
    "Service check…": "Проверка доступа…",
    "Configuration": "Конфигурация",
    "Selected": "Выделенные",
    "Whole tab": "Вся вкладка",
    "check": "проверить",
    "stop": "стоп",
    "opens": "открывается",
    "refused": "отказ",
    "no answer": "нет ответа",
    "Pick the services and press check": "Выберите сервисы и нажмите «проверить»",
    "a check is already running": "проверка уже идёт",
    "no services selected": "не выбран ни один сервис",
    "nothing to check": "нечего проверять",

    "The engine has not said yet": "Движок ещё не сообщил",
    "proxy": "через VPN",
    "direct": "напрямую",
    "blocked": "заблокировано",

    "Right now": "Сейчас",
    "Since tracking began": "За сеанс",
    "Kept in memory only, until you clear it or close the program": "Хранится только в памяти — до очистки или закрытия программы",
    "Last seen": "Последний раз",
    "Nothing yet": "Пока ничего",
    "clear": "очистить",
    "now": "сейчас",
    "s ago": "с назад",

    "How much lower a candidate has to be before AUTO leaves a working server. Used by the two ping modes: in \"by ping\" that is the live round-trip against the candidate's ping, in \"by ping, steady\" it is history against history. Below this margin the two are the same server as far as you are concerned, and switching only costs you the connection you had.":
      "Насколько ниже должен быть кандидат, чтобы AUTO ушёл с работающего сервера. Используется двумя режимами по пингу: в «по пингу» сравнивается текущее время отклика с пингом кандидата, в «по пингу, устойчиво» — история с историей. Меньшая разница для вас неощутима, а переключение стоит установленного соединения.",
    "The same margin for the two speed modes: how much faster a candidate has to be, in per cent. In \"by speed\" that compares the last two measurements, in \"by speed, steady\" the two history scores — the average less its spread. The millisecond margin above is not used in these modes.":
      "Тот же запас для двух режимов по скорости: на сколько процентов кандидат должен быть быстрее. В «по скорости» сравниваются последние замеры, в «по скорости, устойчиво» — оценки по истории, то есть среднее за вычетом разброса. Порог в миллисекундах выше в этих режимах не используется.",
    "How long AUTO must stay on a server before it may leave for a faster one. Both margins and this delay hold back the slow-but-alive case only — a server that stops answering is left as soon as the failure threshold is reached.": "Сколько AUTO обязан продержаться на сервере, прежде чем уйти на более быстрый. Оба порога и эта задержка сдерживают только случай «жив, но медленный»: сервер, переставший отвечать, покидается сразу по достижении порога сбоев.",

    "Connections": "Соединения",
    "Destination": "Назначение",
    "Nothing is going through the tunnel right now": "Сейчас через туннель ничего не идёт",
    "Program": "Программа",
    "Rule": "Правило",
    "Time": "Время",
    "Via": "Куда",
    "In TUN mode what goes through the tunnel is listed here with its byte counts; the rest is read from the engine's own log, which is quiet unless asked. Turn on Count rule matches or Verbose logs and reconnect to also see which program asked, which rule sent it, and the traffic routed around the tunnel.": "В режиме TUN то, что идёт через туннель, попадает сюда вместе со счётчиками байт; остальное читается из журнала самого движка, а тот молчит, пока не попросят. Включите «Считать срабатывания правил» или «Подробные логи» и переподключитесь, чтобы видеть также, какая программа запросила, какое правило её туда отправило и трафик, идущий мимо туннеля.",

    "Switch tolerance (ms)": "Порог переключения (мс)",
    "Switch tolerance (speed, %)": "Порог переключения по скорости (%)",
    "Minimum time on a server (s)": "Минимум времени на сервере (с)",
    "How much better a candidate has to be before AUTO leaves a working server, and how long it must stay before it may leave at all. These hold back the slow-but-alive case only — a server that stops answering is left as soon as the failure threshold is reached.": "Насколько кандидат должен быть лучше, чтобы AUTO ушёл с работающего сервера, и сколько он обязан на нём продержаться. Действует только для случая «жив, но медленный»: сервер, переставший отвечать, покидается сразу по достижении порога сбоев.",

    "Stability": "Стабильность",
    "How the server has behaved over time, not just now": "Как сервер вёл себя всё это время, а не только сейчас",
    "Checks": "Проверок",
    "Average": "Средний",
    "Jitter": "Разброс",
    "Answered": "Отвечал",
    "Too few checks to judge": "Слишком мало проверок для оценки",

    "Works only with this on:": "Работает только при включённой настройке:",

    "Tunnel": "Туннель",
    "Connection": "Соединение",
    "DPI bypass": "Обход DPI",

    "Local proxy": "Локальный прокси",
    "Default 19876. Change it if something else on this machine already listens on that port.": "По умолчанию 19876. Измените, если этот порт на этой машине уже занят.",

    "Proxy kill-switch": "Kill-switch для прокси",
    "Proxy mode only carries the applications that honour the system proxy — everything else has always gone straight out, connected or not. This blocks that at the firewall while a proxy session is up: what stays open is the engine itself and the loopback of this machine, which is where the proxy listens. Needs the same administrator rights TUN mode does, and releases on disconnect.": "Через proxy-режим идут только приложения, которые уважают системный прокси — всё остальное всегда ходило напрямую, подключены вы или нет. Эта настройка перекрывает такой трафик в брандмауэре на время сеанса: открытыми остаются сам движок и локальные адреса этой машины, где и слушает прокси. Нужны те же права администратора, что и для TUN; снимается при отключении.",

    "TUN Keep blocking after the tunnel is gone": "TUN: продолжать блокировать после исчезновения туннеля",
    "The kill-switch above is enforced by the engine, so it ends when the engine does — a crash, or Vair being closed, returns the machine to its ordinary connection without saying so. This holds the block in the firewall instead: while it is engaged, nothing leaves except through the tunnel. It also covers the switch between two configurations, where the old adapter goes away and the ordinary routes come back for a second. Closing Vair does not lift it — that is the case it exists for. It comes down when you say so: disconnecting, turning this off, or the red \"traffic blocked\" button that appears on the connection bar while it is standing. Connection → Restore the connection on start changes who ends that: on, the next start brings the same tunnel back under the block; off, the machine stays blocked until you connect or press the red button. Either way nothing goes out unprotected.":
      "Kill-switch выше обеспечивается самим движком, поэтому и заканчивается вместе с ним: падение или закрытие Vair молча возвращает машину на обычное соединение. Эта настройка вместо этого держит блокировку в брандмауэре: пока она стоит, наружу не уходит ничего, кроме туннеля. Она же закрывает переключение между конфигурациями, когда старый адаптер исчезает и на секунду возвращаются обычные маршруты. Закрытие Vair блокировку не снимает — ради этого случая она и существует. Снимается она по вашему слову: отключением, выключением этой настройки или красной кнопкой «трафик заблокирован» на строке подключения. «Подключение → Восстанавливать подключение при запуске» меняет только то, кто снимет это состояние: с настройкой следующий запуск сам поднимет тот же туннель под блокировкой, без неё машина останется заблокированной, пока вы не подключитесь или не нажмёте красную кнопку. Незащищённым трафик не идёт ни в том, ни в другом случае.",
    "Read this: while the block stands, this machine has no internet except through the tunnel — after a crash, after closing Vair, and after a restart. Starting Vair gives it back: connect, or press the red traffic-blocked button. Without Vair, in PowerShell as administrator:":
      "Прочитайте: пока блокировка стоит, у этой машины нет интернета иначе как через туннель — после падения, после закрытия Vair и после перезагрузки. Запуск Vair возвращает его: подключитесь или нажмите красную кнопку «трафик заблокирован». Без Vair — в PowerShell от имени администратора:",
    "Before you leave this on": "Прежде чем оставить это включённым",
    "With this on, the block lives in the firewall and not in the engine — so it stays after the tunnel is gone. It stays when Vair crashes, when you close Vair, and after a restart of the computer.":
      "С этой настройкой блокировка живёт в брандмауэре, а не в движке, — и остаётся после исчезновения туннеля. Она остаётся при падении Vair, при закрытии Vair и после перезагрузки компьютера.",
    "While it stands, this machine has no internet at all except through the tunnel. Not a slower connection, not some sites: nothing loads.":
      "Пока она стоит, у этой машины нет интернета вообще — только через туннель. Не медленнее, не «часть сайтов»: не открывается ничего.",
    "It comes down when you say so, and only then:": "Снимается она по вашему слову и только так:",
    "disconnect, or turn this setting off;": "отключиться или выключить эту настройку;",
    "press the red \"traffic blocked ✕\" button on the connection bar;":
      "нажать красную кнопку «трафик заблокирован ✕» на строке подключения;",
    "without Vair — in PowerShell as administrator:": "без Vair — в PowerShell от имени администратора:",
    "Connection → Restore the connection on start changes who ends that: on, the next start brings the same tunnel back under the block; off, the machine stays blocked until you connect or press the red button. Either way nothing goes out unprotected.":
      "«Подключение → Восстанавливать подключение при запуске» меняет только то, кто снимет это состояние: с настройкой следующий запуск сам поднимет тот же туннель под блокировкой, без неё машина останется заблокированной, пока вы не подключитесь или не нажмёте красную кнопку. Незащищённым трафик не идёт ни в том, ни в другом случае.",
    "Do not show again": "Больше не показывать",
    "I understand": "Я понял",
    "While the block stands, nothing leaves this machine except through the tunnel — including after Vair is closed. To open it without Vair, in PowerShell as administrator:":
      "Пока блокировка стоит, наружу с этой машины не уходит ничего, кроме туннеля, — в том числе после закрытия Vair. Открыть её без Vair — в PowerShell от имени администратора:",
    "what this means": "что это значит",
    "Traffic is blocked at the firewall — the server stopped responding": "Трафик заблокирован брандмауэром — сервер перестал отвечать",

    "Watch the connected server": "Присматривать за подключённым сервером",
    "Watches whether the connection still carries traffic, and closes it with a desktop notification when it stops. It watches the bytes first — your own traffic crossing the tunnel and coming back is the best answer there is — and only asks the server when they go quiet, or when they leave with nothing coming back. What is on this machine, the engine still running and the tunnel interface still there, is checked always and needs no setting; this is the part that can cost a request, which is why it is yours to switch on. Does not overlap with AUTO: while AUTO is on it does the watching itself and moves to another server rather than closing.":
      "Следит, идёт ли ещё через подключение трафик, и закрывает его с уведомлением на рабочем столе, когда трафик прекращается. Сначала смотрит на байты — ваш собственный трафик, прошедший через туннель и вернувшийся, и есть лучший ответ, — и спрашивает сервер, только когда байты затихли или уходят без ответа. То, что находится на этой машине, — жив ли движок, на месте ли интерфейс туннеля, — проверяется всегда и настройки не требует; здесь же речь о части, которая может стоить запроса, поэтому её включаете вы. С AUTO не пересекается: пока AUTO включён, он следит сам и уходит на другой сервер, а не закрывает подключение.",

    "Count rule matches": "Считать срабатывания правил",
    "Shows next to each rule below how many times it has fired, and marks the ones that never have — usually a typo. Costs real work: the engine has to narrate every routing decision, every DNS lookup and several lines per connection, which on a busy TUN link is thousands of lines a second and noticeable CPU. Leave it off unless you are looking at the counters; the connections panel does not need it. Takes effect on the next connection.": "Показывает у каждого правила ниже, сколько раз оно сработало, и отмечает те, которые не сработали ни разу — обычно это опечатка. Обходится недёшево: движок вынужден описывать в журнале о каждом решении маршрутизации, каждом DNS-запросе и печатать по несколько строк на соединение — на нагруженном TUN это тысячи строк в секунду и заметная нагрузка на ЦП. Держите выключенным, пока не смотрите на счётчики; панели соединений эта настройка не нужна. Применяется при следующем подключении.",
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

    "Show the lines that arrived while the view was held": "Показать строки, пришедшие пока вид удерживался",
    "TCP/IP stack": "Стек TCP/IP",
    "Mixed (system TCP)": "Смешанный (TCP системный)",
    "System stack": "Системный",
    "Who does the TCP work inside the tunnel. gVisor (default) does it in the engine: identical behaviour everywhere, and every packet is handled in user space — which is the CPU cost TUN has and proxy mode does not. System hands TCP back to the operating system and costs a fraction of it; Mixed is system TCP with gVisor UDP. If TUN loads the processor on this machine, try Mixed first. Takes effect on the next connection.":
      "Кто выполняет работу TCP внутри туннеля. gVisor (по умолчанию) — сам движок: одинаковое поведение на всех системах и обработка каждого пакета в пространстве пользователя, а это и есть та нагрузка на ЦП, которой в режиме proxy нет вовсе. «Системный» отдаёт TCP операционной системе и стоит заметно дешевле; «смешанный» — системный TCP и UDP через gVisor. Если TUN нагружает процессор, начните со «смешанного». Применяется при следующем подключении.",
    "Favorites": "Избранное",
    "Copy all": "Копировать все",
    "Copy the configuration": "Скопировать конфигурацию",

    "Show list": "Показать список",
    "Every starred configuration, including the ones no tab holds any more — a star is kept against the server itself and outlives the subscription it came from. Nothing is removed automatically; the list marks what cannot be found and removes it on request.":
      "Все конфигурации со звёздочкой, включая те, которых уже нет ни в одной вкладке: звёздочка ставится самому серверу и переживает подписку, из которой он пришёл. Само ничего не удаляется — список помечает ненайденные и убирает их по вашей команде.",
    "A star is kept against the server itself, so it survives a rename and a list refresh — and it outlives the configuration too. What no tab holds right now is marked below; a source that failed to load looks the same, so nothing is removed on its own.":
      "Звёздочка ставится самому серверу, поэтому переживает переименование и обновление списка — а заодно и саму конфигурацию. Ниже помечено то, чего сейчас нет ни в одной вкладке; подписка, которая не загрузилась, выглядит так же, поэтому само ничего не удаляется.",
    "Nothing is starred.": "Ничего не отмечено.",
    "in no tab": "нет ни в одной вкладке",
    "Remove from favorites": "Убрать из избранного",
    "Remove the": "Убрать",
    "not found": "ненайденных",
    "Show setting descriptions": "Показывать описания настроек",
    "The explanatory text under each setting, here and in the other dialogs — tab settings, auto-connect, measurement history. Turn it off once the settings are familiar and the dialogs become much shorter.":
      "Пояснения под каждой настройкой — здесь и в других окнах: настройки вкладки, авто-подключение, история замеров. Выключите, когда настройки станут привычными: окна станут заметно короче.",
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
    "In TUN mode Vair routes only this PC through the tunnel. Turn this on to also open the HTTP/SOCKS proxy (above) on the Proxy-access address, so another device (a phone) can route through this PC into the VPN while the PC itself stays fully tunnelled. Set Proxy access to \"Allow LAN access\" (or a custom address) for it to be reachable from other devices, and point the device at the HTTP port. Off by default; applies on the next connection.":
      "В TUN-режиме Vair заворачивает в туннель только сам этот компьютер. Включите, чтобы дополнительно открыть HTTP/SOCKS-прокси (выше) на адресе из «Доступ к прокси» — тогда другое устройство (телефон) сможет ходить в интернет через VPN этого ПК, при этом сам ПК остаётся полностью в туннеле. Чтобы прокси был доступен извне, поставьте «Доступ к прокси» в «Доступ из локальной сети» (или свой адрес) и укажите на устройстве HTTP-порт. По умолчанию выключено; применяется при следующем подключении.",
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
    "Check IP": "Проверить IP",
    "Check the public exit IP and country as seen from the other end of the tunnel": "Проверить внешний IP и страну такими, какими их видно с другой стороны туннеля",
    "Connect first: the address is checked through the connection.": "Сначала подключитесь: адрес проверяется через подключение.",
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
    "Filter by name, host, type, transport or security. + combines conditions: germany+ws+tls needs all three (name, transport, security); germany+poland matches either (same column). Use field: to target a column, e.g. name:russia. unlock:chatgpt keeps the servers the last service check found it open on.": "Фильтр по name, host, type, transport или security. + объединяет условия: germany+ws+tls требует все три (имя, транспорт, защита); germany+poland — любое из (один столбец). Префикс поле: задаёт столбец, например name:russia. unlock:chatgpt оставляет серверы, на которых последняя проверка сервисов нашла его открытым.",

    // Hints: one per setting (2.6.0)
    "IPv6": "IPv6",
    "Blocked": "Заблокирован",
    "Through the VPN": "Через VPN",
    "Direct — outside the VPN": "Напрямую — мимо VPN",
    "What happens to IPv6 while you are connected. Blocked (default): the tunnel takes the IPv6 route and refuses it, and names resolve to IPv4 only — nothing IPv6 leaves this machine. Through the VPN: IPv6 goes to your server, which has to support it; if it does not, the log says so and IPv4 carries on regardless. Direct: IPv6 goes around the tunnel with your real address on it — what the program did before this setting existed, and still the answer when you need something reachable only over IPv6 on your own network. Local addresses are unaffected in every mode. In proxy mode only the name half applies: there is no tunnel to hold a route, and traffic that ignores the system proxy was never routed by us. Takes effect on the next connection.":
      "Что происходит с IPv6, пока вы подключены. «Заблокирован» (по умолчанию): туннель забирает маршрут IPv6 себе и отвечает отказом, а имена резолвятся только в IPv4 — наружу по IPv6 не уходит ничего. «Через VPN»: IPv6 идёт на ваш сервер, и тот должен его поддерживать; если не поддерживает, программа пишет об этом в лог, а IPv4 продолжает работать. «Напрямую»: IPv6 идёт мимо туннеля с вашим настоящим адресом — так программа вела себя до появления этой настройки, и так же стоит выбрать, если вам нужен ресурс, доступный только по IPv6, в своей сети. Локальные адреса не затрагиваются ни в одном из режимов. В режиме proxy применима только половина про имена: туннеля, который держал бы маршрут, там нет, а трафик, игнорирующий системный прокси, мы и не маршрутизировали. Применяется при следующем подключении.",
    "Shows the Sources tab, where subscription links are added and refreshed. Turning it off only hides the tab — the subscriptions and everything imported from them stay.":
      "Показывает вкладку Sources, где добавляются и обновляются ссылки-подписки. Выключение лишь прячет вкладку — сами подписки и всё, что из них импортировано, остаются.",
    "The port the local HTTP proxy listens on while connected. Default 10819. Change it if something else on this machine already holds that port. Takes effect on the next connection.":
      "Порт, на котором локальный HTTP-прокси слушает во время подключения. По умолчанию 10819. Измените, если этот порт на машине уже занят другой программой. Применяется при следующем подключении.",
    "The port the local SOCKS5 proxy listens on while connected. Default 10818. Takes effect on the next connection.":
      "Порт, на котором локальный SOCKS5-прокси слушает во время подключения. По умолчанию 10818. Применяется при следующем подключении.",
    "Which addresses may reach the two ports above. Localhost only — this computer and nothing else. Allow LAN access — every device on your network. Custom — one address of this machine. Beyond localhost, use it only on a network you trust: the SOCKS proxy can be given a password below, the HTTP one has none until you set one. Takes effect on the next connection.":
      "С каких адресов доступны два порта выше. «Только этот ПК» — этот компьютер и никто больше. «Доступ из локальной сети» открывает их любому устройству в вашей сети. «Свой адрес» — слушать на одном конкретном адресе этой машины. Всё, кроме localhost, используйте только в сети, которой доверяете: SOCKS-прокси можно защитить паролем ниже, у HTTP пароля нет, пока вы его не зададите. Применяется при следующем подключении.",
    "The one address to listen on — an address this machine already has, e.g. 192.168.0.10. Anything else fails to bind and the connection does not come up.":
      "Единственный адрес, на котором слушать, — из тех, что уже есть у этой машины, например 192.168.0.10. С любым другим привязка не удастся и подключение не поднимется.",
    "The user name a SOCKS5 client has to send. Random when authentication is switched on; type your own if a client needs a particular one. Emptied, it is generated again.":
      "Имя пользователя, которое должен присылать SOCKS5-клиент. Создаётся случайным при включении аутентификации; впишите своё, если клиенту нужно определённое. Если оставить поле пустым, будет сгенерировано новое.",
    "The password that goes with it — enter both in your SOCKS5 client. Reset draws a new pair, and clients still holding the old one are refused until you update them.":
      "Пароль к нему — впишите оба в свой SOCKS5-клиент. Reset выдаёт новую пару, и клиенты со старой получают отказ, пока вы их не обновите.",
    "The user name for Basic auth on the local HTTP proxy. Random when authentication is switched on; type your own if a client needs a particular one. Emptied, it is generated again.":
      "Имя пользователя для Basic-аутентификации локального HTTP-прокси. Создаётся случайным при включении аутентификации; впишите своё, если клиенту нужно определённое. Если оставить поле пустым, будет сгенерировано новое.",
    "The password that goes with it — enter both in your HTTP client. Reset draws a new pair, and clients still holding the old one are refused until you update them.":
      "Пароль к нему — впишите оба в свой HTTP-клиент. Reset выдаёт новую пару, и клиенты со старой получают отказ, пока вы их не обновите.",
    "Resolves the names that do NOT go through the tunnel: the countries a bypass mode leaves direct, and the without-VPN rules. Their answers have to come from where you are — a local site asked through a foreign resolver hands back a foreign copy of itself, or refuses. Blank uses Yandex (77.88.8.8). In a mode that tunnels everything, this server is never used.":
      "Резолвит имена, которые НЕ идут через туннель: страны, оставленные прямыми в режимах-исключениях, и списки «без VPN». Ответ на них должен приходить оттуда, где вы находитесь, — местный сайт, спрошенный через зарубежный резолвер, возвращает зарубежную копию себя или отказывает. Пусто — Яндекс (77.88.8.8). В режиме, где через туннель идёт всё, этот сервер не используется.",
    "Resolves everything that goes through the tunnel, which in most modes is nearly everything. The query travels inside the tunnel, so nobody on your side of it sees the name or can answer in its place, and any resolver your VPN server can reach will do: a DoH address or a plain IP. The list holds well-known ones, including several that refuse to answer for advertising, tracking or malware domains — that blocks them for every program on this machine at once, without an extension in the browser. These are services run by other people, and what each of them filters is their decision. Blank uses Cloudflare, which filters nothing.":
      "Резолвит всё, что идёт через туннель, а в большинстве режимов это почти всё. Запрос идёт внутри туннеля: снаружи ни имени не видно, ни ответить за вас нельзя, — поэтому подойдёт любой резолвер, доступный с VPN-сервера: адрес DoH или обычный IP. В списке — известные, в том числе несколько таких, которые не отвечают на домены рекламы, слежки и вредоносного: это перекрывает их сразу для всех программ на машине, без расширения в браузере. Это чужие сервисы, и что именно каждый из них фильтрует — их решение. Пусто — Cloudflare, он не фильтрует ничего.",
    "Own address…": "Свой адрес…",
    "How many bytes go in each piece of the handshake, as a range \"min-max\" or a single number. Default 100-200; a size is drawn from the range for every piece, so no two handshakes are cut the same way. Empty means the default.":
      "Сколько байт приходится на один кусок рукопожатия — диапазон «min-max» или одно число. По умолчанию 100-200; размер берётся из диапазона для каждого куска, поэтому два рукопожатия не режутся одинаково. Пусто — значение по умолчанию.",
    "The pause between the pieces, in milliseconds, as a range or a single number. Default 10-20. Longer pauses defeat more inspection and make every handshake take that much longer. Empty means the default.":
      "Пауза между кусками в миллисекундах — диапазон или одно число. По умолчанию 10-20. Чем пауза больше, тем больше DPI она обходит и тем дольше каждое рукопожатие. Пусто — значение по умолчанию.",
    "How many configurations are pinged at once. Default 10. Higher gets through a large tab sooner and makes each number noisier, since the tests share one channel. Takes effect on the next test run.":
      "Сколько конфигураций пингуется одновременно. По умолчанию 10. Больше — большая вкладка проверяется быстрее, а каждое значение становится шумнее, потому что тесты делят один канал. Действует со следующего запуска тестов.",
    "How many configurations are speed-tested at once. Default 5. This one divides your bandwidth rather than just sharing a channel: run several together and each measures a fraction of what it would reach alone. Takes effect on the next test run.":
      "Сколько конфигураций проверяется на скорость одновременно. По умолчанию 5. Здесь делится уже не канал, а полоса: если запустить несколько сразу, каждая измерит лишь долю того, что выдала бы в одиночку. Действует со следующего запуска тестов.",
    "Bounds the first request, the unmeasured one that builds the tunnel — TCP plus the TLS or Reality handshake. Raise it when configurations that do work come back \"timeout\". Default 4000 ms.":
      "Ограничивает первый, незамеряемый запрос, который поднимает туннель, — TCP плюс рукопожатие TLS или Reality. Увеличьте, если рабочие конфигурации возвращаются с «timeout». По умолчанию 4000 мс.",
    "Bounds one ping round. Three rounds run against every configuration and the best of them is the number you see. Default 1500 ms.":
      "Ограничивает один раунд пинга. По каждой конфигурации идёт три раунда, и в результат попадает лучший. По умолчанию 1500 мс.",
    "How long the download runs before throughput is worked out. Default 4 s. Longer is steadier and spends that much more traffic on every configuration tested.":
      "Сколько времени идёт скачивание, прежде чем считается скорость. По умолчанию 4 с. Дольше — стабильнее и на столько же больше трафика на каждую проверенную конфигурацию.",
    "What the ping test asks for. Any HTTP answer counts, including an error — only the time it took to arrive is measured. Pick the endpoint your provider routes best.":
      "Что запрашивает пинг-тест. Годится любой HTTP-ответ, в том числе ошибка, — меряется только время до него. Выберите эндпоинт, который ваш провайдер маршрутизирует лучше всего.",
    "An address of your own for the ping test. Emptying the field returns to the list above.":
      "Свой адрес для пинг-теста. Если очистить поле, вернётся выбор из списка выше.",
    "A service of your own. It has to answer with JSON carrying the address; the country and the provider are read from it when they are there. Emptying the field returns to the list above.":
      "Свой сервис. Он должен отвечать JSON с адресом; страна и провайдер читаются оттуда же, если они есть. Если очистить поле, вернётся выбор из списка выше.",
    "The file the speed test downloads. It runs for the number of seconds set above however large the file is, so any sufficiently big one will do — what matters is that the host itself is fast.":
      "Файл, который скачивает speed-тест. Тест длится заданное выше число секунд независимо от размера файла, поэтому подойдёт любой достаточно большой — важно, чтобы был быстрым сам хост.",
    "A file of your own for the speed test. Emptying the field returns to the list above.":
      "Свой файл для speed-теста. Если очистить поле, вернётся выбор из списка выше.",
    "Used when the main URL answers HTTP 429 — public test files rate-limit a whole tab being measured one configuration after another. \"None\" turns the retry off and those configurations are simply reported without a speed.":
      "Используется, когда основная ссылка отвечает HTTP 429: публичные тестовые файлы ограничивают частоту, а вкладка меряется конфигурация за конфигурацией. «None» отключает повтор, и такие конфигурации просто остаются без скорости.",
    "A file of your own for the retry — best on a different host from the main one, or it will be rate-limited at the same moment.":
      "Свой файл для повтора — лучше на другом хосте, чем основной, иначе ограничение придёт на оба в один момент.",
    "This machine has several addresses; the link and the QR code below are built from the one you pick here. Choose the address on the network the other device is actually on — the server itself listens on all of them.":
      "У этой машины несколько адресов; ссылка и QR-код ниже строятся из выбранного здесь. Выберите адрес в той сети, где реально находится второе устройство, — сам сервер слушает на всех.",
    "The address to open on the other device, with the key inside it — treat the link and the QR code as you would a password. \"New key\" issues a different one, and every link or code handed out before stops working: that is how a device you no longer trust is cut off.":
      "Адрес, который нужно открыть на другом устройстве; ключ находится прямо в нём — относитесь к ссылке и QR-коду как к паролю. «New key» выдаёт другой ключ, и все выданные раньше ссылки и коды перестают работать: так отключают устройство, которому больше не доверяете.",
    "Dark or light for the whole interface, applied at once. Vair does not follow the system theme — this is the setting.":
      "Тёмная или светлая тема для всего интерфейса, применяется сразу. Vair не следует за системной темой — эта настройка и есть выбор.",
    "The language of the interface, applied at once — English or Russian. Log lines come from the engines and stay in English.":
      "Язык интерфейса, применяется сразу, — английский или русский. Строки логов приходят от движков и остаются на английском.",
    "Counts the bytes crossing the tunnel, in both modes, and shows them in the main window. Turned off, the counters stop where they are and nothing is recorded.":
      "Считает байты, проходящие через туннель, в обоих режимах и показывает их в основном окне. Если выключить, счётчики останавливаются на текущих значениях и ничего не записывается.",
    "Everything that has gone through the tunnel since this counter was last reset, kept across restarts. \"reset total\" puts it back to zero; the session counter beside it zeroes itself on every connection.":
      "Всё, что прошло через туннель с последнего сброса этого счётчика; сохраняется между запусками. «reset total» обнуляет его; сессионный счётчик рядом обнуляется сам при каждом подключении.",
    "The window's close button hides Vair in the tray instead of ending it, and the connection carries on. Off, closing the window disconnects and quits. Quit from the tray menu either way.":
      "Кнопка закрытия окна прячет Vair в трей, а не завершает его, и подключение продолжает работать. Если выключено, закрытие окна отключает VPN и выходит из программы. Выйти можно из меню в трее в любом случае.",
    "Opens the folder holding the whole state of the program — settings, tabs, measurement history, logs and the downloaded routing lists. Copy it to move Vair by hand, or open it to read a log.":
      "Открывает папку, в которой лежит всё состояние программы — настройки, вкладки, история замеров, логи и скачанные списки маршрутизации. Скопируйте её, чтобы перенести Vair вручную, или загляните, чтобы почитать лог.",
    "Export writes the tabs, their settings and the app settings to a JSON file. Import reads one back and replaces what is there now — the way to move Vair to another computer. The file holds your configurations in the clear, so keep it as you would keep them.":
      "Export записывает вкладки, их настройки и настройки программы в JSON-файл. Import читает такой файл и заменяет то, что есть сейчас, — так Vair переносят на другой компьютер. Конфигурации лежат в файле в открытом виде, поэтому храните его так же, как хранили бы их.",
    "Applies to Import only. Turned off, the file's app settings are taken and your tabs are left alone — for carrying settings over without touching the configurations you already have.":
      "Относится только к Import. Если выключено, из файла берутся настройки программы, а вкладки остаются вашими — чтобы перенести настройки, не трогая уже имеющиеся конфигурации.",
    "Update now fetches that build — through the tunnel when you are connected — checks it against the published checksum, puts it in place of this program and restarts it. Settings, tabs and history stay where they are.":
      "«Update now» скачивает эту сборку — через туннель, если вы подключены, — проверяет её по опубликованной контрольной сумме, ставит на место этой программы и перезапускает. Настройки, вкладки и история остаются на месте.",
    "How many candidates are pinged at once. Default 10. Higher gets through a large pool sooner and makes each number noisier, since the tests share one channel.":
      "Сколько кандидатов пингуется одновременно. По умолчанию 10. Больше — большой пул проверяется быстрее, а каждое значение становится шумнее, потому что тесты делят один канал.",
    "The same setting as Settings → Testing: changing it here changes it there, and it applies to manual PING ALL runs too.":
      "Это та же настройка, что в Settings → Testing: меняя её здесь, вы меняете её и там, и она действует также на ручные запуски PING ALL.",
    "How many candidates are speed-tested at once — only used by the ranking modes that measure speed. Default 5. This one divides your bandwidth rather than sharing a channel: several at once each measure a fraction of what they would reach alone.":
      "Сколько кандидатов проверяется на скорость одновременно — используется только режимами ранжирования, которые меряют скорость. По умолчанию 5. Здесь делится уже не канал, а полоса: несколько сразу — и каждый измерит лишь долю того, что выдал бы в одиночку.",
    "The same setting as Settings → Testing: changing it here changes it there, and it applies to manual SPEED ALL runs too.":
      "Это та же настройка, что в Settings → Testing: меняя её здесь, вы меняете её и там, и она действует также на ручные запуски SPEED ALL.",
    "Which mode auto-connect brings a server up in, including when it switches on its own. \"Remember last\" uses whichever mode you connected in last, so an auto switch does not change it under you.":
      "В каком режиме авто-подключение поднимает сервер, в том числе когда переключается само. «Remember last» использует тот режим, в котором вы подключались последний раз, — так автопереключение не меняет режим за вас.",
    "How long the live connection may go unasked, while auto-connect is on. A limit rather than a schedule: while your own traffic is crossing the tunnel and coming back it has already answered the question, and no probe is sent. The probe is for a quiet connection — and for one that has been sending with nothing coming back, which is asked about at once whatever this says. Default 15 s.":
      "Сколько живое подключение может оставаться неспрошенным, пока включено авто-подключение. Это предел, а не расписание: пока ваш собственный трафик идёт через туннель и возвращается, он уже ответил на вопрос, и проба не отправляется. Проба нужна для тишины — и для случая, когда байты уходят, а обратно ничего не приходит: там она делается сразу, независимо от этого значения. По умолчанию 15 с.",
    "How many probes in a row have to fail before auto-connect gives up on the server and moves to the next one. Default 2 — one failure is often just a lost packet, and switching on it would move you off a working server.":
      "Сколько проверок подряд должно провалиться, прежде чем авто-подключение оставит сервер и перейдёт к следующему. По умолчанию 2 — одна неудача часто означает просто потерянный пакет, и переключение по ней увело бы вас с рабочего сервера.",

    // The description a mark opens, with descriptions switched off.
    "This has to be done on the computer itself — it asks for permission there.":
      "Это делается на самом компьютере — там будет запрошено разрешение.",
    "Added the links; anything written after them on the line was ignored.":
      "Ссылки добавлены; всё, что написано после них в строке, пропущено.",
    "The one lookup that cannot go through the tunnel: the address of the VPN server itself, needed before there is a tunnel to send it through. It leaves over ordinary UDP, in the open — this is the query your provider can see and answer in its own way, so give it a resolver you trust rather than theirs. Blank uses Quad9 (9.9.9.9). A configuration written with an IP address instead of a name needs no lookup at all.":
      "Единственный запрос, который не может пойти через туннель: адрес самого VPN-сервера, нужный до того, как туннель появится. Он уходит открытым, обычным UDP, — именно его провайдер видит и может ответить по-своему, поэтому укажите резолвер, которому доверяете, а не его собственный. Пусто — Quad9 (9.9.9.9). Конфигурации, записанной сразу с IP-адресом, запрос не нужен вовсе.",
    "This switch is on, but the block is not in force on this machine:": "Переключатель включён, но блокировка на этой машине не действует:",
    // The DNS preset lists. Their labels are data in an array, so the checker
    // cannot see them either — keep them in step with SettingsModal.tsx.
    "Cloudflare Family — family.cloudflare-dns.com · malware and adult sites": "Cloudflare Family — family.cloudflare-dns.com · вредоносное и взрослые сайты",
    "Cloudflare · https://1.1.1.1/dns-query — no filtering (default)": "Cloudflare · https://1.1.1.1/dns-query — без фильтрации (по умолчанию)",
    "Google · https://dns.google/dns-query — no filtering": "Google · https://dns.google/dns-query — без фильтрации",
    "Quad9 · https://dns.quad9.net/dns-query — malware": "Quad9 · https://dns.quad9.net/dns-query — вредоносное",
    "Cloudflare · https://security.cloudflare-dns.com/dns-query — malware": "Cloudflare · https://security.cloudflare-dns.com/dns-query — вредоносное",
    "AdGuard · https://dns.adguard-dns.com/dns-query — ads and trackers": "AdGuard · https://dns.adguard-dns.com/dns-query — реклама и трекеры",
    "AdGuard Family · https://family.adguard-dns.com/dns-query — ads, trackers, adult sites": "AdGuard Family · https://family.adguard-dns.com/dns-query — реклама, трекеры, взрослые сайты",
    "Cloudflare Family · https://family.cloudflare-dns.com/dns-query — malware and adult sites": "Cloudflare Family · https://family.cloudflare-dns.com/dns-query — вредоносное и взрослые сайты",
    "Quad9 · 9.9.9.9 (default)": "Quad9 · 9.9.9.9 (по умолчанию)",
    "Cloudflare · 1.1.1.1": "Cloudflare · 1.1.1.1",
    "Google · 8.8.8.8": "Google · 8.8.8.8",
    "AdGuard · 94.140.14.14": "AdGuard · 94.140.14.14",
    "Yandex · 77.88.8.8 (default)": "Яндекс · 77.88.8.8 (по умолчанию)",
    "Quad9 · 9.9.9.9 — malware": "Quad9 · 9.9.9.9 — вредоносное",
    "AdGuard · 94.140.14.14 — ads and trackers": "AdGuard · 94.140.14.14 — реклама и трекеры",
    // The session panel. Its labels and fixed values arrive from the backend
    // as data, so the translation checker cannot see them: they are listed
    // here by hand and must be kept in step with core/session_api.go.
    "Session": "Сессия",
    "Nothing is connected, so this is what the settings would produce.": "Ничего не подключено, поэтому здесь то, что дадут текущие настройки.",
    "settings now say": "в настройках сейчас",
    "What this session is running with": "С чем работает эта сессия",
    "Engines": "Движки",
    "Mode": "Режим",
    "Engine": "Движок",
    "Server": "Сервер",
    "Chain": "Цепочка",
    "Interface": "Интерфейс",
    "Local ports": "Локальные порты",
    "System proxy": "Системный прокси",
    "Own rules (without VPN / through / blocked)": "Свои правила (без VPN / через / блок)",
    "Apps without VPN": "Приложения без VPN",
    "Kill-switch": "Kill-switch",
    "Blocking held after Vair stops": "Блокировка держится после закрытия Vair",
    "LAN through the tunnel": "Локальная сеть через туннель",
    "Proxy shared over LAN": "Прокси раздаётся по локальной сети",
    "Leak protection": "Защита от утечек",
    "FakeIP": "FakeIP",
    "Bootstrap": "Bootstrap",
    "Direct": "Прямой",
    "Through the tunnel": "Через туннель",
    "Firewall block right now": "Блокировка в брандмауэре сейчас",
    "Keep blocking after the tunnel is gone": "Не снимать блокировку после исчезновения туннеля",
    "blocking": "блокирует",
    "not blocking": "не блокирует",
    "blocking, but not in force": "блокирует, но не действует",
    "not connected": "не подключено",
    "present": "на месте",
    "Traffic is blocked by the kill-switch. Click to release it.": "Трафик заблокирован kill-switch. Нажмите, чтобы снять блокировку.",
    "Theme and Language are at the top of this window, on the right — always visible, whichever section is open.":
      "«Тема» и «Язык» — вверху этого окна справа: они видны всегда, в каком бы разделе вы ни были.",
    "Follow the main window": "Как в главном окне",
    "Which mode auto-connect brings a server up in, including when it switches on its own. \"Follow the main window\" uses the proxy/tun choice showing there, so an auto switch never comes up in a mode you did not pick.":
      "В каком режиме авто-подключение поднимает сервер, в том числе при самостоятельном переключении. «Как в главном окне» берёт выбор proxy/tun, который там показан, — и автопереключение никогда не поднимет режим, который вы не выбирали.",
    "releasing…": "снимаем…",
    "traffic blocked ✕": "трафик заблокирован ✕",
    "Restore the connection on start": "Восстанавливать подключение при запуске",
    "Connects again, on the next start, to whatever was connected when the program was closed — the same configuration in the same mode, not a new choice. Off by default: opening a program is not by itself a request to send your traffic somewhere. It pairs with \"Keep blocking after the tunnel is gone\": that block holds across a close either way, and this is what brings the tunnel back under it instead of leaving you to press connect. Auto-connect overrides this — it makes its own choice, and a better one.":
      "При следующем запуске подключается к тому, что было подключено в момент закрытия программы, — к той же конфигурации и в том же режиме, без нового выбора. По умолчанию выключено: запуск программы сам по себе не означает просьбы куда-то направить ваш трафик. Работает в паре с «Не снимать блокировку после исчезновения туннеля»: та блокировка переживает закрытие программы в любом случае, а это возвращает под неё туннель — вместо того чтобы оставить вас нажимать «подключить». Авто-подключение важнее этой настройки: оно делает собственный выбор, и лучший.",
    "Show the description": "Показать описание",
    "Description": "Описание",
    "Options": "Варианты",
    "Examples": "Примеры",

    // Routing modes, all five together — the pop-up lists the ones the dialog
    // shows one at a time.
    "Everything goes through the VPN. Nothing is left on the direct connection but the private addresses of your own network.":
      "Через VPN идёт всё. На прямом соединении остаются только приватные адреса вашей собственной сети.",
    "Regular traffic goes DIRECT; only resources blocked in Russia go through the VPN — matched by a community list, which updates itself, plus your own lists below.":
      "Обычный трафик идёт НАПРЯМУЮ; через VPN — только заблокированное в России: по общедоступному списку, который обновляется сам, плюс ваши списки ниже.",

    // AUTO — what the four ranking modes are compared by.
    "What auto-connect compares when it ranks the candidates, and what it re-compares before leaving the server you are on. The two steady modes weigh several measurements instead of the last one.":
      "Что авто-подключение сравнивает, когда ранжирует кандидатов, и что сравнивает заново, прежде чем уйти с сервера, на котором вы сидите. Два «устойчивых» режима взвешивают несколько замеров, а не последний.",
    "Ranked on its measurement history — click to see it":
      "Ранжируется по истории замеров — нажмите, чтобы посмотреть",

    // The table filter and its worked examples.
    "Hides every row that does not match what you type. One word is searched for everywhere at once: the name, the address, the type, the transport and the security. To search one column only, put its name and a colon in front — name:, host:, type:, transport:, security:. Conditions are joined with a plus. Conditions about different columns must all match; two conditions about the same column mean either of them. Case does not matter and part of a word is enough. The figure beside the box counts the rows left against the tab total.":
      "Скрывает все строки, кроме подходящих под то, что вы напишете. Одно слово ищется сразу везде: в имени, адресе, типе, транспорте и защите. Чтобы искать только в одном столбце, поставьте перед словом его имя с двоеточием — name:, host:, type:, transport:, security:. Условия соединяются плюсом. Условия про разные столбцы должны совпасть все; два условия про один столбец означают «любое из них». Регистр не важен, достаточно части слова. Число рядом с полем показывает, сколько строк осталось из того, что есть во вкладке.",
    "Two columns at once: the name contains germany AND the address starts with 185. Spaces around the plus are allowed.":
      "Два столбца сразу: в имени есть germany И адрес начинается со 185. Пробелы вокруг плюса допускаются.",
    "Anywhere: the name, the address or the type.": "Где угодно: имя, адрес или тип.",
    "The name only — an address that happens to contain it is left out.":
      "Только имя — адрес, в котором случайно встретилось то же слово, не подойдёт.",
    "All three at once: the name, the ws transport and TLS security.":
      "Всё сразу: имя, транспорт ws и защита TLS.",
    "Either of them — two words for the same column widen the search instead of narrowing it.":
      "Любое из двух — два слова про один столбец расширяют поиск, а не сужают его.",
    "Every server whose address starts with that.": "Все серверы, адрес которых начинается так.",
    "One protocol. Bare vless, ws, reality and the like are recognised without the prefix.":
      "Один протокол. Слова vless, ws, reality и подобные распознаются и без префикса.",
    "The servers the last service check found ChatGPT open on. Servers nobody checked are left out — the honest answer for them is unknown.":
      "Серверы, на которых последняя проверка сервисов открыла ChatGPT. Непроверенные не показываются — честный ответ про них «неизвестно»."
}

export function t10(lang: string, en: string): string {
  return lang === 'ru' ? (RU[en] ?? en) : en
}
