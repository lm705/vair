# Vair

[Русский](README.md) | [English](README_EN.md) | **فارسی** | [中文](README_ZH.md)

> ⚠️ این ترجمه توسط هوش مصنوعی انجام شده و ممکن است نادقیق باشد. نسخهٔ [روسی](README.md) مرجع اصلی است.

کلاینت VPN برای آزمایش و اتصال به پیکربندی‌هایی با پروتکل‌های **VLESS، VMess، Trojan، Shadowsocks (شامل SS2022)، Hysteria2، TUIC، WireGuard**. بر پایه [Xray-core](https://github.com/XTLS/Xray-core) و [sing-box](https://github.com/SagerNet/sing-box) ساخته شده است.

**می‌توانید بدون داشتن پیکربندی شخصی شروع کنید:** فهرستی از پیکربندی‌های رایگان از پیش در برنامه هست که هر ساعت به‌روزرسانی و به‌صورت خودکار بررسی می‌شود. در حالت [**AUTO**](https://github.com/lm705/vair/wiki/Авто-подключение) برنامه خودش پیکربندی‌ها را آزمایش می‌کند، به سریع‌ترین گزینهٔ سالم وصل می‌شود و اگر از کار افتاد به گزینهٔ دیگری سوئیچ می‌کند. به‌روزرسانی خودکار تب هم خود فهرست را تازه نگه می‌دارد — موارد جدید بدون دخالت شما اضافه می‌شوند.

> ℹ️ **توجه:** این فهرست داخلی فقط **در خاک روسیه** جمع‌آوری و آزمایش می‌شود، بنابراین ممکن است در کشور شما کار نکند. در این صورت اشتراک‌ها و پیکربندی‌های خودتان را اضافه کنید — همهٔ امکانات زیر با آن‌ها هم دقیقاً به همین شکل کار می‌کند.

پیکربندی‌های خودتان هم کاملاً پشتیبانی می‌شوند: **لینک اشتراک**، فایل و پیکربندی‌های تکی را اضافه کنید، آن‌ها را در تب‌ها مرتب کنید و از همان آزمایش‌ها، مسیریابی و AUTO استفاده کنید.

**Windows و Linux.** در Windows یک فایل `.exe` بدون نیاز به نصب — موتورهای xray-core و sing-box از قبل داخل آن هستند. در Linux بسته‌های `.deb`، `.rpm` و Arch موجود است؛ موتورها همراه بسته نصب و با آن به‌روزرسانی می‌شوند.

> 💡 لطفاً پیشنهادها، اشکالات و هر بازخوردی را در [**Issues**](https://github.com/lm705/vair/issues) مطرح کنید. می‌توانید به هر زبانی که برایتان راحت است بنویسید. برای تماس مستقیم: تلگرام [**t.me/mial705**](https://t.me/mial705).

[![Download](https://img.shields.io/github/downloads/lm705/vair/total?label=Download&logo=github)](https://github.com/lm705/vair/releases)
[![Release](https://img.shields.io/github/v/release/lm705/vair?label=Release)](https://github.com/lm705/vair/releases/latest)
![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20Linux-blue)
[![Stars](https://img.shields.io/github/stars/lm705/vair?style=flat)](https://github.com/lm705/vair/stargazers)

<a href="screenshots/main-window.png"><img src="screenshots/main-window.png" alt="پنجرهٔ اصلی Vair: جدول پیکربندی‌ها با نتایج آزمایش" width="900"></a> <a href="screenshots/auto-panel.png"><img src="screenshots/auto-panel.png" alt="پنل AUTO و تنظیمات مسیریابی" width="450"></a>

## شروع سریع

1. فایل `Vair.exe` را از صفحهٔ [**Releases**](https://github.com/lm705/vair/releases) دانلود و اجرا کنید — نیازی به نصب نیست.
2. تب **SOURCES** را باز کنید و **RELOAD** را بزنید تا فهرست تازه دریافت شود.
3. با **PING ALL** یا **SPEED ALL** آن‌ها را بررسی کنید و به هر گزینهٔ سالمی وصل شوید — یا **AUTO** را روشن کنید تا برنامه خودش این کار را انجام دهد.

جزئیات بیشتر در [شروع کار](https://github.com/lm705/vair/wiki/Начало-работы) در ویکی.

## امکانات

* **پروتکل‌های متعدد** — VLESS / VMess / Trojan / Shadowsocks / SS2022 / Hysteria2 / TUIC / WireGuard، همراه با TLS و Reality.
* **دو حالت** — پروکسی سیستمی (HTTP/SOCKS) و TUN (عبور کل ترافیک از تونل).
* [**اتصال خودکار (AUTO)**](https://github.com/lm705/vair/wiki/Авто-подключение) — خودش به سریع‌ترین پیکربندی سالم وصل می‌شود و در صورت قطع شدن، به گزینهٔ دیگری سوئیچ می‌کند.
* **آزمایش** — اندازه‌گیری تأخیر (ping) و سرعت دانلود برای هر پیکربندی، به‌صورت گروهی هم؛ نتایج بین اجراها ذخیره می‌شوند.
* **مسیریابی انعطاف‌پذیر** — پنج حالت: کل ترافیک از VPN؛ همه‌چیز به‌جز سایت‌های کشورهای انتخابی (🇷🇺 🇨🇳 🇮🇷 🇰🇿)؛ فقط موارد مسدود در روسیه؛ فقط موارد مسدود در چین؛ فقط سایت‌های انتخابی. به‌علاوهٔ قوانین دلخواه برای دامنه‌ها، IP‌ها و برنامه‌ها.
* **زنجیره** — اتصال از طریق چند گره پشت سر هم (شما → گره A → گره B → اینترنت).
* **عبور از DPI** — تکه‌تکه کردن دست‌دهی TLS و انتخاب اثر انگشت TLS مرورگر (uTLS).
* **محافظت در برابر نشت DNS** و kill-switch در حالت TUN (فعلاً kill-switch فقط در Windows).
* **تب‌ها و منابع** — پیکربندی‌ها در تب‌ها مرتب می‌شوند و از اشتراک‌ها، فایل‌ها یا یک مخزن خصوصی GitHub دریافت می‌شوند، با به‌روزرسانی خودکار و فیلترها.
* **فهرست‌های بزرگ** — تا حدود ۲۵۰٬۰۰۰ پیکربندی روان کار می‌کند: ذخیره‌سازی در SQLite و مرتب‌سازی، جست‌وجو و فیلتر آنی.
* **VPN روی گوشی** — اشتراک‌گذاری پروکسی در شبکهٔ محلی از حالت TUN و [کنترل برنامه از گوشی](https://github.com/lm705/pc-vpn-to-phone).
* **امکانات جانبی** — کد QR، ساخت دستی پیکربندی، به‌روزرسانی خودکار برنامه، اجرای خودکار، پوستهٔ تیره و روشن، رابط روسی و انگلیسی.

## مستندات

* [**ویکی**](https://github.com/lm705/vair/wiki) — راهنمای کامل: تنظیمات، حالت‌ها، مسیریابی، زنجیره‌ها، آزمایش، پرسش‌های متداول. *(به زبان روسی)*
* 📱 [**نحوه استفاده از VPN این کامپیوتر روی گوشی**](https://github.com/lm705/pc-vpn-to-phone) — اشتراک‌گذاری از طریق Wi-Fi و دسترسی از هر جا با Tailscale.

## vless_alive.txt

فهرستی از پیکربندی‌های رایگان VLESS که در روسیه کار می‌کنند.

این فهرست از پیش در تب **SOURCES** برنامه بارگذاری شده است. دکمه **RELOAD** آخرین نسخه فایل را دریافت می‌کند.

پیکربندی‌ها هر ساعت به‌روزرسانی می‌شوند و به‌صورت خودکار از نظر دسترس‌پذیری و سرعت روی سروری در یکی از محدودترین مناطق روسیه بررسی می‌شوند. پیکربندی‌های کند یا غیرقابل دسترس حذف می‌شوند. اکثر پیکربندی‌ها در زمان به‌روزرسانی فعال هستند.

> ⚠️ **از ۱۹ اکتبر ۲۰۲۶ این فهرست دیگر هر ساعت به‌روزرسانی نمی‌شود.** محل زندگی من تغییر کرده است (و به شما هم توصیه می‌کنم — زمان کمتر از آن است که به نظر می‌رسد)، به همین دلیل دیگر نمی‌توانم به‌روزرسانی منظم را ادامه دهم. فرد دیگری در وقت آزاد خود و بدون برنامهٔ زمانی مشخص آن را به‌روزرسانی خواهد کرد. بررسی دسترس‌پذیری و سرعت باقی می‌ماند، اما ممکن است پیکربندی‌ها به تازگی گذشته نباشند — بهتر است منابع خودتان را هم اضافه کنید.

پیکربندی‌ها از منابع عمومی و آزاد جمع‌آوری می‌شوند. جمع‌آوری و بررسی خودکار توسط اسکریپت [vair-collector](https://github.com/lm705/vair-collector) انجام می‌شود.

کشور ذکرشده در نام ممکن است با IP خروجی واقعی متفاوت باشد.

## سلب مسئولیت

این نرم‌افزار «همان‌طور که هست» (*as is*) ارائه می‌شود، بدون هیچ‌گونه ضمانت صریح یا ضمنی.

این برنامه یک ابزار فنی برای کار با پروتکل‌های شبکه است. **این تبلیغ، ترویج یا ارائه خدمات VPN نیست.** برنامه یک ارائه‌دهنده VPN نیست، سرور اختصاصی ندارد و خدماتی برای دسترسی به منابع مسدودشده ارائه نمی‌دهد.

فایل `vless_alive.txt` شامل پیکربندی‌هایی است که از منابع عمومی اینترنت جمع‌آوری شده‌اند. نویسنده مالک، اپراتور یا کنترل‌کننده سرورهای فهرست‌شده در پیکربندی‌ها نیست. دسترس‌پذیری، امنیت و حریم خصوصی هنگام استفاده از این سرورها تضمین نمی‌شود.

کاربر مسئولیت کامل رعایت قوانین حوزه قضایی خود را در هنگام استفاده از این نرم‌افزار بر عهده دارد.

>## حمایت مالی
>
>[DALINK](https://dalink.to/lm705) (برای نمایش قیمت به روبل، بدون VPN باز کنید)
>
>اطلاعات آدرس‌های «پرداخت» صرفاً جهت اطلاع‌رسانی ارائه شده است.
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

## آمار مخزن

<a href="https://www.star-history.com/?repos=lm705%2Fvair&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=lm705/vair&type=date&theme=dark&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=lm705/vair&type=date&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=lm705/vair&type=date&legend=top-left&sealed_token=s9Mau_7UPLeko_Ss-eqOe8ikw84z-4uCrMQHEW5p80tUkU11etLD94YMXL309vnuSuOVC--xSK3PZffYkDHUv5Wvfv8irHsn2W6D8NgbQJasCtptbCqpnMyCNbceKc9tV5DGkzozcTxHlkooUzf9pJ-2rKVQ5KFeKz-T79cI3bkcUnYlKKyejM5yuXT1" />
 </picture>
</a>
