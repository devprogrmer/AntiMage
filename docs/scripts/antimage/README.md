# اسکریپت‌های نصب AntiMage

این پوشه اسکریپت‌های نصب، به‌روزرسانی، مدیریت سرویس و مهاجرت AntiMage را نگهداری می‌کند.

## نصب Master با Binary

روی Ubuntu/Debian یا توزیع لینوکسی سازگار، با کاربری دارای sudo اجرا کنید:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-binary.sh | sudo bash -s -- install
```

نسخه مشخص:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-binary.sh | sudo bash -s -- install --version v0.1.1
```

## نصب Master با Docker

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage.sh | sudo bash -s -- install
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage.sh | sudo bash -s -- install --database mysql
```

Pull مستقیم image:

```bash
docker pull ghcr.io/devprogrmer/antimage:latest
docker pull ghcr.io/devprogrmer/antimage:v0.1.1
```

برای اجرای مطمئن، قبل از نصب Docker و Docker Compose را نصب کنید. imageهای Docker باید از طریق انتشار پروژه در دسترس باشند؛ در غیر این صورت نصب Binary را انتخاب کنید.

## نصب Node

نصب Docker Node:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node.sh | sudo bash -s -- install
```

نصب Binary Node:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node-binary.sh | sudo bash -s -- install
```

Binary Node فقط زمانی نصب می‌شود که asset مربوط به معماری سرور در بخش Releases منتشر شده باشد. برای منبع یا انتشار سفارشی می‌توانید `ANTIMAGE_NODE_RELEASE_REPO` را قبل از اجرای دستور تنظیم کنید.

برای چند Node روی یک سرور، نام یکتا بدهید:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/antimage-node-binary.sh | sudo bash -s -- install --name antimage-node-2
```

در Master از بخش Nodes، آدرس سرور Node، پورت gRPC/API و گواهی‌های mTLS را ثبت کنید و تا نمایش وضعیت Connected صبر کنید.

## مدیریت و به‌روزرسانی

```bash
sudo antimage status
sudo antimage logs
sudo antimage restart
sudo antimage update
sudo antimage update --version v0.1.1
sudo antimage core-update
```

قبل از update از `/var/lib/antimage` و دیتابیس نسخه پشتیبان بگیرید. اسکریپت‌های نصب را با `sudo bash -c "$(curl ...)"` اجرا نکنید؛ pipe کردن curl به bash مانند نمونه‌های بالا سازگارتر است.

## مهاجرت

قبل از مهاجرت، compose، دیتابیس، تنظیمات و certificateها را backup کنید:

```bash
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/migrate_marzban_to_antimage.sh | sudo bash -s --
curl -fsSL https://raw.githubusercontent.com/devprogrmer/AntiMage/main/scripts/antimage/migrate_marzban_node_to_antimage.sh | sudo bash -s --
```
