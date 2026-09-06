# راهنمای نصب و راه‌اندازی AntiMage

این راهنما مسیر کامل نصب پنل، ساخت مدیر، افزودن Node، تنظیم دامنه و استفاده از اشتراک‌ها را توضیح می‌دهد.

## معماری پیشنهادی

- **Master:** پنل وب، API، کاربران، سرویس‌ها، اشتراک‌ها و مدیریت Nodeها.
- **Node:** سرور اجرای هسته‌ها و سرویس‌های VPN که از Master فرمان می‌گیرد.
- **دیتابیس:** SQLite برای نصب کوچک و MySQL/MariaDB برای نصب‌های بزرگ‌تر.

## پیش‌نیازها

برای Master و هر Node یک سرور لینوکس با دسترسی root یا sudo، معماری amd64 یا arm64، دسترسی خروجی اینترنت و ساعت صحیح سیستم لازم است. برای استفاده عمومی، یک دامنه و گواهی HTTPS تهیه کنید.

## نصب Master با Binary

روی سرور Master اجرا کنید:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-binary.sh | sudo bash -s -- install
```

نصب نسخه مشخص:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-binary.sh | sudo bash -s -- install --version v0.1.1
```

مسیرهای مهم:

| مورد | مسیر پیش‌فرض |
| --- | --- |
| برنامه و تنظیمات نصب | `/opt/antimage` |
| داده، certificate و backup | `/var/lib/antimage` |
| فایل محیطی | `/opt/antimage/.env` |
| سرویس systemd | `antimage.service` |
| پورت Gateway | `8000` |

## نصب Master با Docker

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage.sh | sudo bash -s -- install
```

برای pull مستقیم image:

```bash
docker pull ghcr.io/devprogrmer/antimage:latest
docker pull ghcr.io/devprogrmer/antimage:v0.1.1
```

برای MySQL:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage.sh | sudo bash -s -- install --database mysql
```

قبل از نصب Docker و Docker Compose را نصب و اجرای سرویس Docker را فعال کنید. بعد از انتشار release جدید، تگ نسخه روی GHCR ساخته می‌شود؛ اگر image نسخه مورد نظر هنوز در رجیستری انتشار داده نشده است، از نصب Binary استفاده کنید.

## ساخت حساب مدیر

پس از نصب Master:

```bash
sudo antimage-cli admin create --username admin --role full_access --password 'رمز-قوی-و-منحصربه‌فرد'
```

سپس وارد مسیر زیر شوید:

```text
https://panel.example.com/dashboard/login
```

در محیط واقعی پورت ۸۰۰۰ را فقط از reverse proxy قابل دسترس کنید و آن را بدون HTTPS عمومی نکنید.

## نصب Binary Node

روی سرور Node اجرا کنید:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node-binary.sh | sudo bash -s -- install
```

برای Node دوم روی همان سرور:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node-binary.sh | sudo bash -s -- install --name antimage-node-2
```

این نصب‌کننده asset مناسب معماری سرور را از Releases دریافت می‌کند. اگر نسخه‌ای در Releases موجود نیست، ابتدا همان نسخه را در پروژه منتشر کنید یا مقدار `ANTIMAGE_NODE_RELEASE_REPO` را به مخزن انتشار خود تغییر دهید.

مسیرهای معمول Node:

| مورد | مسیر پیش‌فرض |
| --- | --- |
| برنامه | `/opt/antimage-node` |
| داده و certificate | `/var/lib/antimage-node` |
| سرویس | `antimage-node.service` |

## اتصال Node به Master

1. در داشبورد Master به بخش **Nodes** بروید.
2. یک Node جدید بسازید و نام، آدرس عمومی، پورت سرویس و پورت API/gRPC را وارد کنید.
3. certificate و کلیدهای mTLS تولید یا بارگذاری‌شده را فقط بین Master و Node جابه‌جا کنید.
4. تنظیمات را ذخیره کنید و منتظر بمانید وضعیت Node به **Connected** تغییر کند.
5. سپس هسته، inbound و سرویس‌های مورد نظر را از داشبورد روی Node اعمال کنید.

پورت API/gRPC همان پورتی است که در تنظیمات Node نمایش داده می‌شود؛ مقدار آن را حدس نزنید. فقط پورت‌های مورد نیاز را در firewall باز کنید و دسترسی را به IP Master محدود کنید.

## تنظیمات محیطی

فایل `/opt/antimage/.env` نمونه‌ای شبیه زیر دارد:

```dotenv
UVICORN_HOST=0.0.0.0
UVICORN_PORT=8000
SQLALCHEMY_DATABASE_URL=sqlite:///db.sqlite3
ANTIMAGE_DATA_DIR=/var/lib/antimage
ANTIMAGE_CERT_BASE=/var/lib/antimage/certs
ANTIMAGE_CONFIG_DIR=/etc/antimage
JWT_ACCESS_TOKEN_EXPIRE_MINUTES=1440
```

برای SQLite مسیر مطلق استفاده کنید تا محل فایل دیتابیس روشن باشد. در نصب MySQL مقدار `SQLALCHEMY_DATABASE_URL` را با URL امن دیتابیس خود جایگزین کنید و رمز را داخل Git یا پیام عمومی قرار ندهید.

## دامنه و HTTPS با Nginx

نمونه reverse proxy:

```nginx
server {
    listen 443 ssl http2;
    server_name panel.example.com;

    ssl_certificate /etc/letsencrypt/live/panel.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/panel.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
    }
}
```

قبل از فعال‌سازی، DNS دامنه را به IP Master وصل کنید و گواهی معتبر بگیرید.

## اشتراک کاربران

برای هر کاربر، لینک اشتراک را از صفحه کاربر کپی کنید. صفحه اشتراک با توجه به سیستم‌عامل و User-Agent، برنامه‌ها و روش واردکردن مناسب همان دستگاه را نمایش می‌دهد و اطلاعات قابل استفاده کانفیگ را ارائه می‌کند.

خروجی‌ها می‌توانند شامل لینک‌ها و قالب‌های رایج Xray/V2Ray، Sing-box، Clash و لینک مستقیم پروتکل‌ها باشند. پشتیبانی عملی هر کانفیگ به پروتکل، transport، TLS و قابلیت کلاینت مقصد وابسته است؛ قبل از توزیع عمومی، لینک را با کلاینت هدف آزمایش کنید.

## مدیریت سرویس

```bash
sudo antimage status
sudo antimage logs
sudo antimage restart
sudo antimage update
sudo antimage update --version v0.1.1
sudo antimage core-update
```

برای Node از CLI مربوط به Node استفاده کنید:

```bash
sudo antimage-node status
sudo antimage-node logs
sudo antimage-node restart
```

## Backup و ارتقا

قبل از هر ارتقا این موارد را backup کنید:

- `/var/lib/antimage`
- `/opt/antimage/.env`
- دیتابیس MySQL/MariaDB در صورت استفاده
- certificateها و کلیدهای Master و Node

پس از ارتقا، ورود به داشبورد، سلامت Node، ساخت یک کاربر آزمایشی و دریافت اشتراک را بررسی کنید.

## عیب‌یابی سریع

| نشانه | بررسی |
| --- | --- |
| صفحه باز نمی‌شود | `sudo antimage status`، پورت ۸۰۰۰، firewall و Nginx را بررسی کنید. |
| ورود خطا می‌دهد | مدیر را با `antimage-cli admin list` بررسی و در صورت نیاز مدیر جدید بسازید. |
| Node متصل نمی‌شود | آدرس، پورت API/gRPC، ساعت سیستم، certificate و دسترسی firewall را بررسی کنید. |
| مصرف یا عملیات دیر به‌روز می‌شود | لاگ Master و Node و وضعیت صف عملیات را بررسی کنید. |
| اشتراک در کلاینت کار نمی‌کند | قالب خروجی، transport، TLS/SNI و سازگاری کلاینت مقصد را بررسی کنید. |

## حمایت از توسعه

| شبکه | آدرس |
| --- | --- |
| TON | `UQBUIbaYP0MfRys9AC6vJoAlSXu1a0feylJNg-M2-XYJ-0dC` |
| USDT TRC20 | `THhizvBJD4SjZEVD3KvUpjW1PjbxcpqDzM` |

## مجوز

AntiMage تحت GNU AGPL-3.0 منتشر می‌شود. متن مجوز و notices مربوط به اجزای ثالث در پوشه `LICENSES/` قرار دارد.
