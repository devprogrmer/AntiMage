<p align="center">
  <img src="dashboard/src/assets/logo.svg" width="128" height="128" alt="AntiMage logo">
</p>

<h1 align="center">AntiMage</h1>

<p align="center">
  پنل مستقل مدیریت VPN، کاربران، اشتراک‌ها و زیرساخت چندنودی
</p>

<p align="center">
  <a href="https://github.com/devprogrmer/AntiMage/actions"><img src="https://img.shields.io/github/actions/workflow/status/devprogrmer/AntiMage/binary-build.yml?branch=main&style=flat-square&label=build" alt="Build status"></a>
  <a href="https://github.com/devprogrmer/AntiMage/releases"><img src="https://img.shields.io/github/v/release/devprogrmer/AntiMage?style=flat-square&label=release" alt="Latest release"></a>
  <a href="https://github.com/devprogrmer/AntiMage/blob/main/LICENSE"><img src="https://img.shields.io/github/license/devprogrmer/AntiMage?style=flat-square" alt="License"></a>
  <a href="https://github.com/devprogrmer/AntiMage/stargazers"><img src="https://img.shields.io/github/stars/devprogrmer/AntiMage?style=flat-square" alt="GitHub stars"></a>
</p>

AntiMage یک پنل مستقل برای مدیریت سرویس‌های VPN، کاربران، اشتراک‌ها و نودهای
چندنودی است. این پروژه برای نصب واقعی روی سرور Master و مدیریت نودهای جداگانه
طراحی شده است.

## نصب سریع Master با Binary

روی سرور Master لینوکس اجرا کنید:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-binary.sh | sudo bash -s -- install
```

برای نصب نسخه مشخص:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-binary.sh | sudo bash -s -- install --version v0.1.1
```

نصب‌کننده باینری، سرویس systemd، فایل تنظیمات و مسیر داده را آماده می‌کند:

- برنامه: `/opt/antimage`
- تنظیمات: `/opt/antimage/.env`
- داده‌ها، گواهی‌ها و backup: `/var/lib/antimage`
- پورت پیش‌فرض: `8000`

## نصب Master با Docker

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage.sh | sudo bash -s -- install
```

اگر Docker image را مستقیم می‌خواهید:

```bash
docker pull ghcr.io/devprogrmer/antimage:latest
docker pull ghcr.io/devprogrmer/antimage:v0.1.1
```

برای MySQL یا MariaDB:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage.sh | sudo bash -s -- install --database mysql
```

نصب Docker به image منتشرشده نیاز دارد. بعد از انتشار release جدید، تگ نسخه روی GHCR ساخته می‌شود؛ اگر image نسخه مورد نظر در رجیستری موجود نیست، از نصب Binary استفاده کنید.

## ساخت مدیر و ورود

بعد از نصب Master:

```bash
sudo antimage-cli admin create --username admin --role full_access --password 'رمز-قوی-خودتان'
```

سپس باز کنید:

```text
https://panel.example.com/dashboard/login
```

برای محیط واقعی، پنل را پشت HTTPS و reverse proxy قرار دهید و پورت مدیریت را
مستقیماً روی اینترنت عمومی باز نگذارید.

## نصب Binary Node

روی هر سروری که قرار است نود VPN باشد اجرا کنید:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node-binary.sh | sudo bash -s -- install
```

برای چند نود روی یک سرور، نام جداگانه بدهید:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node-binary.sh | sudo bash -s -- install --name antimage-node-2
```

مسیرهای معمول نود:

- برنامه: `/opt/antimage-node`
- داده و certificate: `/var/lib/antimage-node`
- سرویس: `antimage-node.service`

بعد از نصب، در داشبورد Master به بخش Nodes بروید، نود را ثبت کنید و وضعیت آن
را تا Connected بررسی کنید. کلیدها و certificateهای واقعی را عمومی نکنید.

نصب Binary Node به asset مناسب معماری سرور در بخش Releases نیاز دارد؛ در صورت
نبود asset، مقدار `ANTIMAGE_NODE_RELEASE_REPO` را به مخزن انتشار خود تنظیم کنید.

## نصب Docker Node

اگر نود را با Docker می‌خواهید بالا بیاورید:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node.sh | sudo bash -s -- install
```

برای pull مستقیم image نود:

```bash
docker pull ghcr.io/devprogrmer/antimage-node:latest
docker pull ghcr.io/devprogrmer/antimage-node:v0.1.1
```

## تنظیمات اصلی

نمونه فایل محیطی:

```dotenv
UVICORN_HOST=0.0.0.0
UVICORN_PORT=8000
SQLALCHEMY_DATABASE_URL=sqlite:///db.sqlite3
ANTIMAGE_GATEWAY_ADDR=:8000
ANTIMAGE_DATA_DIR=/var/lib/antimage
ANTIMAGE_CERT_BASE=/var/lib/antimage/certs
ANTIMAGE_CONFIG_DIR=/etc/antimage
JWT_ACCESS_TOKEN_EXPIRE_MINUTES=1440
```

برای سرور کوچک SQLite کافی است. برای نصب بزرگ‌تر Docker با MySQL یا MariaDB
انتخاب مناسب‌تری است. قبل از ارتقا از دیتابیس، تنظیمات و certificateها backup بگیرید.

## تنظیم دامنه و HTTPS با Nginx

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

## قابلیت‌ها

- مدیریت کاربران، حجم، انقضا و مصرف دوره‌ای
- مدیریت چند نود، health check، صف عملیات و مشاهده وضعیت اعمال‌شده
- ورودی و خروجی‌های Xray و تنظیمات مسیر‌دهی
- WireGuard، OpenVPN، L2TP/IPsec، PPTP، IKEv2 و AnyConnect
- اشتراک هوشمند برای iOS، Android، Windows، macOS و Linux
- خروجی‌های V2Ray، Sing-box، Clash و لینک‌های مستقیم پروتکل
- QR، جزئیات کامل کانفیگ، API، نقش‌ها، حسابرسی، backup و اعلان‌ها

## به‌روزرسانی

```bash
sudo antimage update
sudo antimage restart
```

یا نسخه مشخص:

```bash
sudo antimage update --version v0.1.1
```

قبل از ارتقا سرویس را در محیط تست بررسی و از `/var/lib/antimage` backup بگیرید.

## مستندات کامل

راهنمای مرحله‌به‌مرحله فارسی در [docs/README-fa.md](docs/README-fa.md) قرار دارد.
راهنمای اسکریپت‌ها در [docs/scripts/antimage/README.md](docs/scripts/antimage/README.md) است.

## حمایت از توسعه

اگر AntiMage برای شما مفید است، می‌توانید از ادامه توسعه و نگهداری آن حمایت کنید:

| شبکه | آدرس کیف پول |
| --- | --- |
| TON | `UQBUIbaYP0MfRys9AC6vJoAlSXu1a0feylJNg-M2-XYJ-0dC` |
| USDT TRC20 | `THhizvBJD4SjZEVD3KvUpjW1PjbxcpqDzM` |

## مجوز

AntiMage تحت GNU AGPL-3.0 منتشر می‌شود. متن مجوز در پوشه `LICENSES/` قرار دارد.
