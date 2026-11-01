import {
	Alert,
	AlertIcon,
	Badge,
	Box,
	Button,
	Container,
	Flex,
	Grid,
	GridItem,
	Heading,
	HStack,
	Input,
	InputGroup,
	InputLeftElement,
	Progress,
	SimpleGrid,
	Stack,
	Text,
	VStack,
} from "@chakra-ui/react";
import {
	ArrowLeftIcon,
	ArrowRightIcon,
	BookOpenIcon,
	CheckCircleIcon,
	MagnifyingGlassIcon,
	PlayCircleIcon,
	ShieldCheckIcon,
} from "@heroicons/react/24/outline";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useHref, useSearchParams } from "react-router-dom";

type Language = "fa" | "en" | "ru";
type Lesson = {
	id: string;
	group: string;
	title: string;
	location: string;
	goal: string;
	steps: [string, string, string];
	goodToKnow: string;
};

const lessons: Record<Language, Lesson[]> = {
	fa: [
		{
			id: "overview",
			group: "شروع کار",
			title: "آشنایی با داشبورد",
			location: "داشبورد",
			goal: "وضعیت پنل، سرورها و مصرف را از یک صفحه بخوانید.",
			steps: [
				"بعد از ورود، کارت‌های CPU، حافظه، دیسک و Swap را ببینید.",
				"بخش مصرف شبکه، وضعیت سرویس‌ها و نمودارهای تاریخی را برای تشخیص روند بررسی کنید.",
				"اگر عددی غیرعادی است، بازه زمانی و سرور مربوط را مشخص کنید و سپس به صفحه نود یا لاگ بروید.",
			],
			goodToKnow:
				"نمودار داشبورد تصویر یک بازه است؛ برای عیب‌یابی، زمان نمونه و وضعیت همان سرویس را هم بررسی کنید.",
		},
		{
			id: "users",
			group: "کاربران و دسترسی",
			title: "فهرست، جست‌وجو و فیلتر کاربران",
			location: "کاربران ← فهرست کاربران",
			goal: "کاربر موردنظر را پیدا کنید و وضعیت حساب را درست بخوانید.",
			steps: [
				"از جست‌وجو برای نام، شناسه یا اطلاعات اشتراک استفاده کنید.",
				"فیلتر سرویس، وضعیت و تاریخ را تنظیم کنید؛ در صورت نیاز ستون‌ها و مرتب‌سازی را تغییر دهید.",
				"روی ردیف کاربر بزنید تا اطلاعات، مصرف، تنظیمات و عملیات همان حساب را ببینید.",
			],
			goodToKnow:
				"وضعیت محدودیت، انقضا و تعلیق یکسان نیستند؛ قبل از تغییر، مقدار و تاریخ انقضا را در جزئیات بررسی کنید.",
		},
		{
			id: "create-user",
			group: "کاربران و دسترسی",
			title: "ساخت اولین کاربر",
			location: "کاربران ← ساخت کاربر",
			goal: "یک حساب بسازید، سرویس و محدودیت‌هایش را تعیین کنید و اشتراک تحویل دهید.",
			steps: [
				"ساخت کاربر را بزنید؛ نام، سرویس، تاریخ انقضا و وضعیت اولیه را وارد کنید.",
				"حجم، محدودیت اتصال/دستگاه و یادداشت داخلی را با نیاز حساب تنظیم کنید.",
				"ذخیره کنید؛ سپس لینک اشتراک یا QR را از عملیات همان کاربر کپی و با کاربر به‌اشتراک بگذارید.",
			],
			goodToKnow:
				"برای حسابی که هنوز تحویل نشده، وضعیت On hold را انتخاب کنید تا از فعال‌سازی زودهنگام جلوگیری شود.",
		},
		{
			id: "lifecycle",
			group: "کاربران و دسترسی",
			title: "تمدید، تعلیق و پایان حساب",
			location: "کاربران ← جزئیات کاربر",
			goal: "چرخه عمر یک حساب را بدون اشتباه در تاریخ یا سرویس مدیریت کنید.",
			steps: [
				"کاربر را باز کنید و تاریخ پایان یا طرح بعدی را بررسی کنید.",
				"برای تمدید، زمان یا طرح بعدی را ثبت و ذخیره کنید؛ برای توقف موقت از تعلیق استفاده کنید.",
				"پس از تغییر، وضعیت و تاریخ جدید را دوباره در فهرست و جزئیات تأیید کنید.",
			],
			goodToKnow:
				"حذف حساب برگشت‌پذیر نیست مگر اینکه نسخه پشتیبان مناسب داشته باشید؛ ابتدا تعلیق یا لغو اشتراک را در نظر بگیرید.",
		},
		{
			id: "services",
			group: "زیرساخت و مسیر اتصال",
			title: "سرویس و مسیرهای اتصال",
			location: "سرویس‌ها",
			goal: "مشخص کنید کاربران هر سرویس از کدام میزبان‌ها و مسیرها استفاده می‌کنند.",
			steps: [
				"سرویس را باز کنید و میزبان‌های متصل و ترتیب آن‌ها را بررسی کنید.",
				"برای ساخت یا ویرایش، نام و تنظیمات سرویس را وارد کنید و میزبان‌های در دسترس را انتخاب کنید.",
				"ذخیره کنید و یک اشتراک آزمایشی از همان سرویس را بررسی کنید تا خروجی مسیر درست باشد.",
			],
			goodToKnow:
				"تغییر میزبان‌های سرویس می‌تواند محتوای اشتراک را عوض کند؛ قبل از ذخیره، اثر آن روی کاربران سرویس را بررسی کنید.",
		},
		{
			id: "inbounds",
			group: "زیرساخت و مسیر اتصال",
			title: "اینباندها و میزبان‌ها",
			location: "اینباندها و هاست‌ها",
			goal: "پورت‌ها و ورودی‌های پروتکل را به میزبان‌های اشتراک متصل کنید.",
			steps: [
				"در صفحه اینباندها، پروتکل، پورت، شبکه و وضعیت ورودی را بررسی کنید.",
				"در صفحه میزبان‌ها، آدرس عمومی و برچسب اینباند متناظر را وارد کنید.",
				"یک کاربر آزمایشی بسازید و لینک تولیدشده را بازبینی کنید؛ پورت و نام میزبان باید با مقصد واقعی سازگار باشد.",
			],
			goodToKnow:
				"برای WireGuard، خالی‌گذاشتن MTU فقط وقتی درست است که نسخه/پیکربندی نود اجازه مقدار پیش‌فرض را بدهد؛ رفتار نود مقصد را قبل از استفاده عمومی آزمایش کنید.",
		},
		{
			id: "nodes",
			group: "زیرساخت و مسیر اتصال",
			title: "افزودن و عیب‌یابی نود",
			location: "نودها ← تنظیمات نود",
			goal: "ارتباط امن پنل و نود را برقرار کنید و وضعیت سرویس‌های نود را بخوانید.",
			steps: [
				"افزودن نود را بزنید؛ نام، آدرس و پورت‌های سرویس و API را مطابق سرور وارد کنید.",
				"بسته نصب گواهی همان نود را دریافت و فقط روی همان سرور نصب کنید.",
				"به صفحه نود برگردید؛ Connected و زمان آخرین ارتباط را ببینید و برای خطا، پیام وضعیت و لاگ را باز کنید.",
			],
			goodToKnow:
				"بسته نصب شامل کلید خصوصی است؛ آن را در چت عمومی نفرستید و برای نود دیگری استفاده نکنید.",
		},
		{
			id: "usage",
			group: "مصرف و حسابداری",
			title: "مصرف ترافیک و سهمیه",
			location: "مصرف / جزئیات کاربر",
			goal: "مصرف ثبت‌شده را با سهمیه و وضعیت کاربر تطبیق دهید.",
			steps: [
				"صفحه مصرف را باز کنید و بازه، سرویس، نود و کاربر را فیلتر کنید.",
				"مصرف کل و جزئیات نودها را مقایسه کنید؛ اختلاف کوتاه‌مدت ممکن است ناشی از زمان همگام‌سازی باشد.",
				"برای رسیدگی به حساب، رکورد کاربر و آخرین زمان دریافت آمار نود را نیز بررسی کنید.",
			],
			goodToKnow:
				"محدودیت حجم به رفتار runtime وابسته است؛ برای تأیید قطع، وضعیت اتصال جاری و نتیجه تلاش اتصال تازه را جداگانه بررسی کنید.",
		},
		{
			id: "backup",
			group: "نگهداری و امنیت",
			title: "ساخت و بازگردانی نسخه پشتیبان",
			location: "داشبورد ← پشتیبان‌گیری و بازیابی",
			goal: "فایل بکاپ بسازید و پیش از Restore محدوده بازگردانی را بفهمید.",
			steps: [
				"ساخت بکاپ را بزنید و منتظر بمانید تا عملیات با وضعیت موفق تمام شود.",
				"فایل را دریافت کنید و اندازه، زمان ساخت و قرارداشتن آن در محل امن را تأیید کنید.",
				"برای Restore، فایل معتبر را انتخاب کنید، پیش‌نمایش/هشدار محدوده را بخوانید و فقط سپس عملیات را آغاز کنید.",
			],
			goodToKnow:
				"Restore می‌تواند داده‌های موجود را جایگزین کند؛ ابتدا از وضعیت فعلی نسخه پشتیبان بگیرید و صفحه نتیجه عملیات را تا پایان دنبال کنید.",
		},
		{
			id: "admins",
			group: "کاربران و دسترسی",
			title: "ادمین‌ها، نقش‌ها و مجوزها",
			location: "مدیران",
			goal: "دسترسی هر اپراتور را به حداقل بخش‌های لازم محدود کنید.",
			steps: [
				"مدیران را باز کنید و نقش، سرویس‌های مجاز و وضعیت هر حساب را مرور کنید.",
				"برای افزودن اپراتور، نقش و محدوده دسترسی را مشخص و رمز اولیه امن تعیین کنید.",
				"با حساب یا نمای مجوزها بررسی کنید که بخش‌های خارج از محدوده در دسترس نیستند.",
			],
			goodToKnow:
				"کلید API و رمز عبور را در یادداشت کاربر یا لینک اشتراک ذخیره نکنید.",
		},
		{
			id: "settings",
			group: "نگهداری و امنیت",
			title: "تنظیمات پنل و Xray",
			location: "تنظیمات پنل / تنظیمات نود / تنظیمات Xray",
			goal: "تنظیمات مؤثر بر پنل و runtime را با مسیر امن تغییر دهید.",
			steps: [
				"بخش تنظیمات مربوط را انتخاب کنید و مقدار فعلی را پیش از تغییر ثبت کنید.",
				"فقط یک تغییر را اعمال و ذخیره کنید؛ پیام نتیجه و وضعیت همگام‌سازی نود را بخوانید.",
				"اتصال آزمایشی و لاگ همان نود را بررسی کنید؛ اگر مشکل دیدید، مقدار قبلی را بازگردانید.",
			],
			goodToKnow:
				"تنظیمات runtime ممکن است روی اتصال جاری اثر بگذارد؛ تغییر را در زمان مناسب و ابتدا روی یک مسیر آزمایشی انجام دهید.",
		},
		{
			id: "integrations",
			group: "نگهداری و امنیت",
			title: "ربات، اعلان‌ها و یکپارچه‌سازی‌ها",
			location: "تنظیمات پنل ← یکپارچه‌سازی‌ها",
			goal: "کانال‌های اعلان و اتصال‌های بیرونی را به‌صورت امن تنظیم کنید.",
			steps: [
				"یکپارچه‌سازی موردنظر را باز کنید و فیلدهای لازم و مجوزهای آن را مرور کنید.",
				"توکن یا نشانی را در فیلد محرمانه وارد و ذخیره کنید؛ آن را در اسکرین‌شات منتشر نکنید.",
				"یک اعلان آزمایشی بفرستید و گزارش موفقیت یا خطای اتصال را بررسی کنید.",
			],
			goodToKnow:
				"اگر اعتبار اتصال افشا شد، آن را از سرویس مقصد باطل و مقدار تازه را در پنل ثبت کنید.",
		},
		{
			id: "logs",
			group: "عیب‌یابی",
			title: "لاگ‌ها و عملیات اخیر",
			location: "لاگ‌ها / عملیات اخیر",
			goal: "خطا را به زمان، کاربر و نود مرتبط کنید.",
			steps: [
				"زمان تقریبی خطا را یادداشت و همان بازه را در لاگ‌ها انتخاب کنید.",
				"با نام کاربر، سرویس یا شناسه عملیات جست‌وجو کنید؛ چند خط قبل و بعد خطا را هم بخوانید.",
				"اگر تغییری قابل بازگشت است از عملیات اخیر استفاده کنید؛ پیش از اقدام اثر و منبع تغییر را بررسی کنید.",
			],
			goodToKnow:
				"متن لاگ ممکن است آدرس IP یا شناسه حساس داشته باشد؛ قبل از ارسال عمومی آن را پاک‌سازی کنید.",
		},
		{
			id: "insights",
			group: "مصرف و حسابداری",
			title: "تحلیل اتصال و آمار",
			location: "مصرف / بینش دسترسی",
			goal: "نوسان اتصال و ترافیک را با بازه زمانی درست بررسی کنید.",
			steps: [
				"بازه‌ای شامل زمان رخداد انتخاب کنید و کاربر یا نود را محدود کنید.",
				"نمودار آنلاین‌بودن را کنار داده مصرف و وضعیت سلامت نود بخوانید.",
				"برای نتیجه‌گیری، چند نمونه زمانی را مقایسه کنید؛ یک عکس لحظه‌ای به‌تنهایی روند را ثابت نمی‌کند.",
			],
			goodToKnow:
				"VPN معمولاً نام دقیق برنامه‌های کاربر را نمی‌بیند؛ آمار شبکه را با فهرست برنامه‌ها اشتباه نگیرید.",
		},
		{
			id: "api",
			group: "توسعه و خودکارسازی",
			title: "استفاده امن از API",
			location: "API و مستندات",
			goal: "مسیر، دسترسی و قالب درخواست را پیش از اتصال نرم‌افزار بیرونی پیدا کنید.",
			steps: [
				"در مرجع API، نام عملیات یا مسیر را جست‌وجو و روش و توضیحات را بخوانید.",
				"برای اتصال بیرونی از کلید API شخصی و محدود استفاده کنید؛ کلید را در سمت سرور نگه دارید.",
				"اول درخواست خواندنی را آزمایش کنید و پاسخ خطا، مجوز و محدودیت را بررسی کنید.",
			],
			goodToKnow:
				"درخواست‌های تغییردهنده می‌توانند کاربر یا runtime را عوض کنند؛ آن‌ها را روی داده واقعی بدون نسخه پشتیبان آزمایش نکنید.",
		},
		{
			id: "account",
			group: "حساب شخصی",
			title: "امن‌سازی حساب خودتان",
			location: "حساب من",
			goal: "رمز، نشست‌ها و کلیدهای API خود را مدیریت کنید.",
			steps: [
				"حساب من را باز کنید و نشست‌های فعال و تنظیمات امنیتی را مرور کنید.",
				"در صورت پشتیبانی، تأیید دومرحله‌ای را فعال و کدهای بازیابی را خارج از سرور امن نگه دارید.",
				"کلیدهای API را با نام کاربرد بسازید، سطح دسترسی کم بدهید و کلیدهای بلااستفاده را لغو کنید.",
			],
			goodToKnow:
				"پس از تغییر رمز، نشست‌های قدیمی ممکن است باطل شوند؛ پیش از خروج مطمئن شوید رمز تازه را امن نگه داشته‌اید.",
		},
		{
			id: "bulk",
			group: "کاربران و دسترسی",
			title: "عملیات گروهی کاربران",
			location: "عملیات گروهی",
			goal: "یک تغییر گروهی را با فیلتر محدود و قابل‌بررسی انجام دهید.",
			steps: [
				"ابتدا سرویس، وضعیت و معیارهای جست‌وجو را محدود کنید تا فقط حساب‌های هدف دیده شوند.",
				"کاربران را انتخاب و نوع عملیات را بخوانید؛ تعداد انتخاب‌شده و خلاصه تغییر را دوباره بررسی کنید.",
				"اجرا را تأیید و نتیجه هر مورد را در گزارش عملیات بررسی کنید.",
			],
			goodToKnow:
				"تغییر گروهی تاریخ، حجم یا سرویس می‌تواند روی تعداد زیادی حساب اثر بگذارد؛ پیش از تأیید، فهرست انتخاب‌شده را بازبینی کنید.",
		},
		{
			id: "haproxy",
			group: "زیرساخت و مسیر اتصال",
			title: "مدیریت HAProxy",
			location: "HAProxy",
			goal: "نودها و مسیرهای HAProxy را با بررسی سلامت و نتیجه تغییر مدیریت کنید.",
			steps: [
				"صفحه HAProxy را باز کنید و وضعیت، نودهای عضو و آخرین سلامت را بررسی کنید.",
				"برای ساخت یا ویرایش، نام و مقصدهای درست را وارد و قبل از ذخیره اعتبارسنجی کنید.",
				"پس از ذخیره، وضعیت نودها و تست اتصال مسیر را دوباره ببینید.",
			],
			goodToKnow:
				"تغییر مسیر پراکسی می‌تواند ترافیک کاربران متصل را جابه‌جا کند؛ آدرس مقصد و امکان بازگشت را پیش از اعمال بررسی کنید.",
		},
		{
			id: "external-apps",
			group: "نگهداری و امنیت",
			title: "برنامه‌ها و فایل‌های خارجی",
			location: "برنامه‌های خارجی",
			goal: "فایل‌های یک برنامه متصل را ببینید یا به‌روزرسانی کنید.",
			steps: [
				"برنامه را از فهرست باز کنید و وضعیت اتصال و مسیر فایل‌های آن را بخوانید.",
				"پیش از بارگذاری، فایل و مقصد را بررسی کنید؛ فایل‌های لازم را جایگزین کنید.",
				"نتیجه عملیات را بخوانید و سلامت برنامه را پس از تغییر تأیید کنید.",
			],
			goodToKnow:
				"بارگذاری فایل پیکربندی می‌تواند سرویس بیرونی را تغییر دهد؛ نسخه فعلی را پیش از جایگزینی نگه دارید.",
		},
		{
			id: "subscriptions",
			group: "کاربران و دسترسی",
			title: "لینک اشتراک و قالب کلاینت",
			location: "کاربران ← لینک اشتراک / تنظیمات Xray",
			goal: "خروجی اشتراک را برای کلاینت هدف بررسی کنید.",
			steps: [
				"از جزئیات کاربر لینک اشتراک را کپی کنید و مطمئن شوید سرویس و میزبان‌های کاربر درست‌اند.",
				"در صورت نیاز، قالب یا فرمت کلاینت را در تنظیمات مربوط بررسی کنید.",
				"خروجی را با یک کلاینت آزمایشی وارد کنید و نام میزبان، پروتکل و اعتبار را تأیید کنید.",
			],
			goodToKnow:
				"لینک اشتراک مانند رمز دسترسی است؛ فقط از مسیر امن به صاحب حساب بدهید و هنگام افشا آن را تعویض کنید.",
		},
		{
			id: "roles",
			group: "کاربران و دسترسی",
			title: "نقش و محدوده دسترسی ادمین",
			location: "مدیران ← جزئیات ادمین",
			goal: "نقش و محدوده‌ی سرویس اپراتور را طوری تنظیم کنید که فقط کارهای مجاز انجام شوند.",
			steps: [
				"در فهرست مدیران، ادمین را باز کنید و نقش فعلی و سرویس‌های اختصاص‌یافته را ببینید.",
				"ویرایش را بزنید؛ فقط مجوزها و سرویس‌های لازم را فعال و محدودیت‌های حساب را مرور کنید.",
				"ذخیره کنید و با دسترسی همان نقش، صفحه‌های مجاز و غیرمجاز را بررسی کنید.",
			],
			goodToKnow:
				"تغییر Full access یا دسترسی مدیریت ادمین اثر گسترده دارد؛ برای اپراتور روزمره این نقش را انتخاب نکنید.",
		},
		{
			id: "telegram",
			group: "نگهداری و امنیت",
			title: "اعلان تلگرام و نسخه پشتیبان",
			location: "تنظیمات پنل ← تلگرام",
			goal: "ربات را به پنل متصل و ارسال اعلان یا نسخه پشتیبان را بررسی کنید.",
			steps: [
				"توکن ربات و مقصد را از تنظیمات تلگرام وارد کنید و ذخیره را بزنید.",
				"از گزینه تست استفاده کنید و دریافت پیام در مقصد درست را تأیید کنید.",
				"برای بکاپ زمان‌بندی‌شده، مقصد و آخرین نتیجه را بررسی کنید؛ یک فایل را هم جداگانه دریافت و اعتبارسنجی کنید.",
			],
			goodToKnow:
				"توکن ربات دسترسی می‌دهد؛ در تصویر یا پیام عمومی قرار ندهید و در صورت افشا آن را تعویض کنید.",
		},
		{
			id: "xray-logs",
			group: "عیب‌یابی",
			title: "لاگ‌های Xray و بررسی اتصال",
			location: "لاگ‌های Xray",
			goal: "خطاهای runtime را جدا از رویدادهای مدیریتی پنل پیدا کنید.",
			steps: [
				"صفحه لاگ‌های Xray را باز کنید و نود و بازه‌ی زمانی رخداد را انتخاب کنید.",
				"پیام‌های همان زمان را با وضعیت نود و کاربر مقایسه کنید؛ خطای احراز هویت با خطای دسترسی شبکه یکی نیست.",
				"بعد از تغییر، همان نود را دوباره بررسی و با یک اتصال آزمایشی نتیجه را تأیید کنید.",
			],
			goodToKnow:
				"لاگ شامل شناسه و IP است؛ پیش از ارسال برای پشتیبانی اطلاعات شناسایی را حذف کنید.",
		},
		{
			id: "database",
			group: "نگهداری و امنیت",
			title: "ابزار پایگاه داده",
			location: "phpMyAdmin",
			goal: "در صورت نیاز اطلاعات پایگاه داده را فقط با مجوز مناسب بررسی کنید.",
			steps: [
				"ابزار را فقط از پنل و برای کار مدیریتی مشخص باز کنید.",
				"برای بررسی، جدول و رکورد را بخوانید؛ تغییر مستقیم داده‌ها ممکن است قوانین مالی و همگام‌سازی نود را دور بزند.",
				"برای تغییر داده از امکانات رسمی پنل یا مهاجرت پشتیبانی‌شده استفاده کنید و پس از آن سلامت پنل را کنترل کنید.",
			],
			goodToKnow:
				"ویرایش یا حذف مستقیم ردیف‌ها می‌تواند شمارش ترافیک، سهمیه، اشتراک یا دسترسی کاربران را ناسازگار کند.",
		},
	],
	en: [
		{
			id: "overview",
			group: "Start here",
			title: "Read the dashboard",
			location: "Dashboard",
			goal: "Understand panel, server, and traffic status from one place.",
			steps: [
				"After signing in, review CPU, memory, disk, and swap cards.",
				"Check network usage, service health, and historical charts for trends.",
				"If a value looks unusual, note its time and server, then open the relevant node or logs.",
			],
			goodToKnow:
				"A dashboard chart represents a time window; use the sample time and service state when diagnosing.",
		},
		{
			id: "users",
			group: "Users and access",
			title: "Find and filter users",
			location: "Users → User list",
			goal: "Find the right account and understand its state.",
			steps: [
				"Search by the user name, identifier, or subscription details.",
				"Filter by service, status, or date; adjust columns and sorting when needed.",
				"Open a row to review that account’s settings, usage, and available actions.",
			],
			goodToKnow:
				"Limited, expired, and on-hold are different states. Check the value and expiry date before changing one.",
		},
		{
			id: "create-user",
			group: "Users and access",
			title: "Create your first user",
			location: "Users → Create user",
			goal: "Create an account, assign its service and limits, then share its subscription.",
			steps: [
				"Select Create user; enter a name, service, expiry date, and initial status.",
				"Set traffic, connection/device limits, and an internal note for the account.",
				"Save, then copy the subscription link or QR code from that user’s actions.",
			],
			goodToKnow:
				"Use On hold for an account that is not ready to activate yet.",
		},
		{
			id: "lifecycle",
			group: "Users and access",
			title: "Renew, suspend, or end an account",
			location: "Users → User details",
			goal: "Manage an account lifecycle without changing the wrong date or service.",
			steps: [
				"Open the user and review the expiry date and next plan.",
				"For renewal, set the new period or plan and save; use suspension for a temporary pause.",
				"Confirm the new date and state in both details and the user list.",
			],
			goodToKnow:
				"Deletion may not be reversible. Consider suspending or revoking a subscription first.",
		},
		{
			id: "services",
			group: "Network and routing",
			title: "Services and connection routes",
			location: "Services",
			goal: "Choose which hosts and routes users of a service receive.",
			steps: [
				"Open a service and review its assigned hosts and their order.",
				"Create or edit a service, then select the hosts users should receive.",
				"Save and inspect a test subscription from that service.",
			],
			goodToKnow:
				"Changing service hosts can change subscription output for its users.",
		},
		{
			id: "inbounds",
			group: "Network and routing",
			title: "Inbounds and hosts",
			location: "Inbounds and Hosts",
			goal: "Connect protocol inbounds to public subscription hosts.",
			steps: [
				"Review each inbound’s protocol, port, network, and enabled state.",
				"Add a public host with the matching inbound tag.",
				"Inspect a test user’s generated link and confirm host and port.",
			],
			goodToKnow:
				"A blank WireGuard MTU is valid only when the target node/runtime supports its default. Verify on that node first.",
		},
		{
			id: "nodes",
			group: "Network and routing",
			title: "Add and troubleshoot a node",
			location: "Nodes → Node settings",
			goal: "Connect the panel to a node and read its runtime health.",
			steps: [
				"Add a node with the reachable address and matching service and API ports.",
				"Download that node’s certificate install bundle and install it only on that server.",
				"Return to Nodes, check Connected and last contact, and open the node status or logs if needed.",
			],
			goodToKnow:
				"The install bundle contains a private key. Do not post it publicly or reuse it for another node.",
		},
		{
			id: "usage",
			group: "Usage and accounting",
			title: "Traffic usage and quotas",
			location: "Usage / User details",
			goal: "Compare recorded traffic with the user’s quota and state.",
			steps: [
				"Open Usage and filter by period, service, node, and user.",
				"Compare totals with per-node details; short delays can reflect sync timing.",
				"For an account issue, also check the user record and the node’s last statistics time.",
			],
			goodToKnow:
				"Quota behavior depends on the runtime. Check current-session closure separately from new-connection rejection.",
		},
		{
			id: "backup",
			group: "Security and maintenance",
			title: "Create and restore a backup",
			location: "Dashboard → Backup and restore",
			goal: "Create a backup and understand restore scope before starting it.",
			steps: [
				"Select Create backup and wait for the operation to finish successfully.",
				"Download the file and verify its size, timestamp, and secure storage location.",
				"For restore, choose a valid file, review the scope warning, and then start the operation.",
			],
			goodToKnow:
				"Restore can replace existing data. Make a current backup first and follow the operation status to completion.",
		},
		{
			id: "admins",
			group: "Users and access",
			title: "Admins, roles, and permissions",
			location: "Admins",
			goal: "Give each operator only the access they need.",
			steps: [
				"Review each admin’s role, allowed services, and account state.",
				"When adding an operator, choose a role and scope and set a secure initial password.",
				"Verify that sections outside the assigned scope are unavailable.",
			],
			goodToKnow:
				"Never store passwords or API keys in user notes or subscription links.",
		},
		{
			id: "settings",
			group: "Security and maintenance",
			title: "Panel and Xray settings",
			location: "Panel settings / Node settings / Xray settings",
			goal: "Change panel and runtime settings with a safe verification path.",
			steps: [
				"Open the relevant settings section and note the current value.",
				"Make and save one change at a time; read the result and node sync status.",
				"Test a connection and check that node’s logs; restore the old value if needed.",
			],
			goodToKnow:
				"Runtime changes may affect active connections. Test one route first and choose a suitable maintenance window.",
		},
		{
			id: "integrations",
			group: "Security and maintenance",
			title: "Bots, notifications, and integrations",
			location: "Panel settings → Integrations",
			goal: "Configure notification channels and external connections safely.",
			steps: [
				"Open the integration and review its required fields and permissions.",
				"Enter the secret in its protected field and save it; do not share it in screenshots.",
				"Send a test notification and review the connection result.",
			],
			goodToKnow:
				"If a credential is exposed, revoke it at the provider and replace it in the panel.",
		},
		{
			id: "logs",
			group: "Troubleshooting",
			title: "Logs and recent actions",
			location: "Logs / Recent actions",
			goal: "Connect an error with its time, user, and node.",
			steps: [
				"Note when the issue happened and select the matching log window.",
				"Search by user, service, or operation ID; include nearby lines for context.",
				"Use a reversible action only after checking its actor and effect.",
			],
			goodToKnow:
				"Logs may include IP addresses or identifiers. Remove sensitive details before sharing them.",
		},
		{
			id: "insights",
			group: "Usage and accounting",
			title: "Connection and usage insights",
			location: "Usage / Access insights",
			goal: "Review connection changes and traffic over the right time window.",
			steps: [
				"Choose a period that includes the event and filter to the relevant user or node.",
				"Read the online chart alongside usage and node health.",
				"Compare multiple samples before deciding that a pattern is persistent.",
			],
			goodToKnow:
				"A VPN usually cannot identify the exact apps a user opens; network usage is not an app list.",
		},
		{
			id: "api",
			group: "Automation",
			title: "Use the API safely",
			location: "API and docs",
			goal: "Find an operation and its access requirements before connecting software.",
			steps: [
				"Search the API reference by operation name or route and review its method and description.",
				"Use a personal, narrowly scoped API key for integrations and keep it server-side.",
				"Test a read-only request first and inspect permission and validation errors.",
			],
			goodToKnow:
				"Mutation requests can change users or runtime state. Do not experiment on live data without a backup.",
		},
		{
			id: "account",
			group: "Your account",
			title: "Secure your own account",
			location: "My account",
			goal: "Manage your password, sessions, and API keys.",
			steps: [
				"Open My account and review active sessions and security settings.",
				"Enable two-factor authentication when available and store recovery codes securely.",
				"Name API keys by purpose, grant minimal access, and revoke unused keys.",
			],
			goodToKnow:
				"Changing your password may revoke older sessions. Keep the new password secure before signing out.",
		},
		{
			id: "bulk",
			group: "Users and access",
			title: "Bulk user actions",
			location: "Bulk actions",
			goal: "Apply a group change using a narrow, reviewable selection.",
			steps: [
				"Filter by service, state, and search criteria so only the intended accounts remain.",
				"Select users and review the action, selected count, and change summary.",
				"Confirm and inspect the result for every item in the operation report.",
			],
			goodToKnow:
				"A bulk expiry, traffic, or service change can affect many accounts. Review the selected list before confirming.",
		},
		{
			id: "haproxy",
			group: "Network and routing",
			title: "Manage HAProxy",
			location: "HAProxy",
			goal: "Manage HAProxy nodes and routes while checking health and results.",
			steps: [
				"Open HAProxy and review its state, member nodes, and latest health.",
				"When creating or editing, verify names and destinations before saving.",
				"After saving, check node status and test the route again.",
			],
			goodToKnow:
				"Changing a proxy route can move live traffic. Verify the destination and rollback path first.",
		},
		{
			id: "external-apps",
			group: "Security and maintenance",
			title: "External apps and files",
			location: "External apps",
			goal: "Inspect or update files used by a connected external app.",
			steps: [
				"Open the app and review connection state and its file area.",
				"Check the file and destination before uploading or replacing it.",
				"Read the operation result and verify app health after the change.",
			],
			goodToKnow:
				"Replacing a configuration file can change an external service. Keep its current version first.",
		},
		{
			id: "subscriptions",
			group: "Users and access",
			title: "Subscription links and client formats",
			location: "Users → Subscription link / Xray settings",
			goal: "Check subscription output for the intended client.",
			steps: [
				"Copy a user’s subscription link and confirm the assigned service and hosts.",
				"Review the relevant client format or template setting when needed.",
				"Import the output into a test client and verify host, protocol, and validity.",
			],
			goodToKnow:
				"A subscription link grants access. Share it privately and rotate it if exposed.",
		},
		{
			id: "roles",
			group: "Users and access",
			title: "Admin roles and scope",
			location: "Admins → Admin details",
			goal: "Limit an operator to the permissions and services required for their work.",
			steps: [
				"Open the admin and review the current role and assigned services.",
				"Choose Edit; enable only required permissions and check account limits.",
				"Save and verify which pages the role can and cannot access.",
			],
			goodToKnow:
				"Full access and admin-management permissions have broad impact. Avoid assigning them for routine operations.",
		},
		{
			id: "telegram",
			group: "Security and maintenance",
			title: "Telegram alerts and backups",
			location: "Panel settings → Telegram",
			goal: "Connect a bot and verify notifications or backup delivery.",
			steps: [
				"Enter the bot token and destination in Telegram settings and save.",
				"Send a test message and verify it reaches the intended destination.",
				"For scheduled backups, review destination and last result; download and validate a backup separately.",
			],
			goodToKnow:
				"A bot token grants access. Do not share it publicly; replace it if exposed.",
		},
		{
			id: "xray-logs",
			group: "Troubleshooting",
			title: "Xray logs and connection checks",
			location: "Xray logs",
			goal: "Find runtime errors separately from panel administration events.",
			steps: [
				"Open Xray logs and choose the affected node and time range.",
				"Compare messages with node health and the user state; authentication and network errors differ.",
				"After a change, review the same node and verify with a test connection.",
			],
			goodToKnow:
				"Logs can contain identifiers and IP addresses. Remove them before sharing externally.",
		},
		{
			id: "database",
			group: "Security and maintenance",
			title: "Database tools",
			location: "phpMyAdmin",
			goal: "Inspect database records only when an administrative task requires it.",
			steps: [
				"Open the tool from the panel only for a specific, authorized task.",
				"Prefer reading records; direct edits can bypass accounting and node synchronization rules.",
				"Use panel actions or supported migrations for changes, then verify panel health.",
			],
			goodToKnow:
				"Direct row edits can make traffic, quotas, subscriptions, or user access inconsistent.",
		},
	],
	ru: [
		{
			id: "overview",
			group: "Начало работы",
			title: "Обзор панели",
			location: "Панель управления",
			goal: "Проверяйте состояние панели, серверов и трафика на одном экране.",
			steps: [
				"После входа проверьте карточки CPU, памяти, диска и Swap.",
				"Посмотрите использование сети, состояние служб и историю показателей.",
				"Если значение выглядит необычно, отметьте время и сервер, затем откройте узел или журнал.",
			],
			goodToKnow:
				"График относится к выбранному периоду; при диагностике учитывайте время и состояние службы.",
		},
		{
			id: "users",
			group: "Пользователи и доступ",
			title: "Поиск и фильтры пользователей",
			location: "Пользователи → Список",
			goal: "Найдите аккаунт и правильно прочитайте его состояние.",
			steps: [
				"Ищите по имени, идентификатору или данным подписки.",
				"Фильтруйте по сервису, состоянию или дате; при необходимости измените столбцы и сортировку.",
				"Откройте строку, чтобы увидеть настройки, трафик и действия аккаунта.",
			],
			goodToKnow:
				"Ограниченный, истёкший и ожидающий аккаунт — разные состояния. Проверьте дату и статус.",
		},
		{
			id: "create-user",
			group: "Пользователи и доступ",
			title: "Создание первого пользователя",
			location: "Пользователи → Создать",
			goal: "Создайте аккаунт, назначьте сервис и лимиты, затем передайте подписку.",
			steps: [
				"Нажмите «Создать пользователя» и укажите имя, сервис, срок и начальный статус.",
				"Задайте лимит трафика, подключений/устройств и внутреннюю заметку.",
				"Сохраните и скопируйте ссылку подписки или QR-код в действиях пользователя.",
			],
			goodToKnow:
				"Используйте статус On hold, если аккаунт ещё не готов к активации.",
		},
		{
			id: "lifecycle",
			group: "Пользователи и доступ",
			title: "Продление и приостановка",
			location: "Пользователи → Данные пользователя",
			goal: "Управляйте сроком и состоянием аккаунта.",
			steps: [
				"Откройте пользователя и проверьте срок действия и следующий план.",
				"Для продления укажите новый срок или план; для паузы используйте приостановку.",
				"Проверьте новый статус и дату в карточке и списке.",
			],
			goodToKnow:
				"Удаление может быть необратимым. Сначала рассмотрите приостановку или отзыв подписки.",
		},
		{
			id: "services",
			group: "Сеть и маршруты",
			title: "Сервисы и маршруты подключения",
			location: "Сервисы",
			goal: "Настройте узлы и хосты, доступные пользователям сервиса.",
			steps: [
				"Откройте сервис и проверьте назначенные хосты и их порядок.",
				"При создании или редактировании выберите нужные хосты.",
				"Сохраните и проверьте тестовую подписку сервиса.",
			],
			goodToKnow:
				"Изменение хостов сервиса может изменить подписки его пользователей.",
		},
		{
			id: "inbounds",
			group: "Сеть и маршруты",
			title: "Inbound и хосты",
			location: "Inbounds и хосты",
			goal: "Свяжите входы протоколов с публичными адресами подписок.",
			steps: [
				"Проверьте протокол, порт, сеть и состояние inbound.",
				"Добавьте публичный хост с соответствующей меткой inbound.",
				"Проверьте ссылку тестового пользователя и совпадение адреса и порта.",
			],
			goodToKnow:
				"Пустой MTU WireGuard подходит только если узел использует значение по умолчанию. Проверьте его заранее.",
		},
		{
			id: "nodes",
			group: "Сеть и маршруты",
			title: "Добавление и проверка узла",
			location: "Узлы → Настройки узла",
			goal: "Подключите узел и проверьте состояние его служб.",
			steps: [
				"Добавьте узел с доступным адресом и совпадающими портами службы и API.",
				"Скачайте сертификат именно этого узла и установите его только на этот сервер.",
				"Проверьте Connected и время связи; при ошибке откройте статус и журнал узла.",
			],
			goodToKnow:
				"Пакет установки содержит закрытый ключ. Не публикуйте его и не используйте на другом узле.",
		},
		{
			id: "usage",
			group: "Трафик и учёт",
			title: "Трафик и лимиты",
			location: "Использование / Данные пользователя",
			goal: "Сравните учтённый трафик с лимитом пользователя.",
			steps: [
				"Откройте статистику и задайте период, сервис, узел и пользователя.",
				"Сравните общий и узловой трафик; небольшая задержка может быть связана с синхронизацией.",
				"При проблеме проверьте запись пользователя и время последней статистики узла.",
			],
			goodToKnow:
				"Проверяйте закрытие активного подключения отдельно от отказа новых подключений.",
		},
		{
			id: "backup",
			group: "Безопасность и обслуживание",
			title: "Резервная копия и восстановление",
			location: "Панель → Резервное копирование",
			goal: "Создайте копию и проверьте область восстановления.",
			steps: [
				"Создайте резервную копию и дождитесь успешного завершения операции.",
				"Скачайте файл и проверьте размер, время и безопасное место хранения.",
				"Перед восстановлением выберите файл, прочитайте предупреждение и запустите операцию.",
			],
			goodToKnow:
				"Восстановление может заменить данные. Сначала сделайте актуальную копию и следите за статусом.",
		},
		{
			id: "admins",
			group: "Пользователи и доступ",
			title: "Администраторы и права",
			location: "Администраторы",
			goal: "Ограничьте доступ оператора необходимыми разделами.",
			steps: [
				"Проверьте роль, доступные сервисы и состояние каждого аккаунта.",
				"При добавлении выберите роль и область доступа и задайте надёжный пароль.",
				"Убедитесь, что разделы вне области доступа скрыты.",
			],
			goodToKnow:
				"Не храните пароли и API-ключи в заметках пользователей или ссылках подписки.",
		},
		{
			id: "settings",
			group: "Безопасность и обслуживание",
			title: "Настройки панели и Xray",
			location: "Настройки панели / узла / Xray",
			goal: "Меняйте настройки с проверкой результата.",
			steps: [
				"Откройте нужный раздел и запишите текущее значение.",
				"Меняйте по одному параметру и проверьте сообщение и синхронизацию узла.",
				"Проверьте подключение и журнал; при проблеме верните старое значение.",
			],
			goodToKnow:
				"Настройки runtime могут повлиять на активные соединения. Сначала проверьте один маршрут.",
		},
		{
			id: "integrations",
			group: "Безопасность и обслуживание",
			title: "Уведомления и интеграции",
			location: "Настройки панели → Интеграции",
			goal: "Подключите внешние службы и каналы уведомлений безопасно.",
			steps: [
				"Откройте интеграцию и проверьте поля и нужные права.",
				"Введите секрет в защищённое поле и сохраните; не публикуйте его на снимках экрана.",
				"Отправьте тестовое уведомление и проверьте результат.",
			],
			goodToKnow:
				"Если секрет раскрыт, отзовите его у провайдера и замените в панели.",
		},
		{
			id: "logs",
			group: "Диагностика",
			title: "Журналы и последние действия",
			location: "Журналы / Последние действия",
			goal: "Свяжите ошибку со временем, пользователем и узлом.",
			steps: [
				"Запишите время проблемы и выберите соответствующий период журнала.",
				"Ищите пользователя, сервис или ID операции и прочитайте соседние строки.",
				"Перед обратимым действием проверьте автора и его последствия.",
			],
			goodToKnow:
				"В журналах могут быть IP и идентификаторы. Удалите их перед публикацией.",
		},
		{
			id: "insights",
			group: "Трафик и учёт",
			title: "Анализ подключений",
			location: "Использование / Анализ доступа",
			goal: "Проверяйте подключения и трафик в нужном периоде.",
			steps: [
				"Выберите период события и отфильтруйте пользователя или узел.",
				"Сопоставьте график активности с трафиком и здоровьем узла.",
				"Сравните несколько измерений, прежде чем считать проблему постоянной.",
			],
			goodToKnow:
				"VPN обычно не определяет точные приложения пользователя; сетевой трафик — не список приложений.",
		},
		{
			id: "api",
			group: "Автоматизация",
			title: "Безопасная работа с API",
			location: "API и документация",
			goal: "Изучите метод, маршрут и права до подключения интеграции.",
			steps: [
				"Найдите операцию по имени или пути и прочитайте метод и описание.",
				"Используйте личный API-ключ с минимальными правами и храните его на сервере.",
				"Сначала проверьте чтение и изучите ошибки доступа и проверки данных.",
			],
			goodToKnow:
				"Запросы на изменение могут менять пользователей и runtime. Не тестируйте их на реальных данных без копии.",
		},
		{
			id: "account",
			group: "Ваш аккаунт",
			title: "Защита личного аккаунта",
			location: "Мой аккаунт",
			goal: "Управляйте паролем, сессиями и API-ключами.",
			steps: [
				"Откройте аккаунт и проверьте активные сессии и параметры безопасности.",
				"Включите двухэтапную проверку, если она доступна, и храните коды восстановления безопасно.",
				"Назначайте ключам понятные имена и минимальные права; ненужные ключи отзывайте.",
			],
			goodToKnow:
				"Смена пароля может завершить старые сессии. Сохраните новый пароль до выхода.",
		},
		{
			id: "bulk",
			group: "Пользователи и доступ",
			title: "Групповые действия",
			location: "Групповые действия",
			goal: "Выполните массовое изменение с ограниченным списком аккаунтов.",
			steps: [
				"Сначала отфильтруйте сервис и состояние, чтобы оставить нужные аккаунты.",
				"Выберите пользователей и проверьте действие, количество и сводку изменений.",
				"Подтвердите операцию и проверьте результат каждого элемента.",
			],
			goodToKnow:
				"Массовое изменение срока, трафика или сервиса затрагивает много аккаунтов. Проверьте список.",
		},
		{
			id: "haproxy",
			group: "Сеть и маршруты",
			title: "Управление HAProxy",
			location: "HAProxy",
			goal: "Управляйте узлами и маршрутами, проверяя их состояние.",
			steps: [
				"Откройте страницу и проверьте узлы и последнее состояние здоровья.",
				"При создании или редактировании проверьте адреса назначения.",
				"После сохранения проверьте состояние и подключение к маршруту.",
			],
			goodToKnow:
				"Изменение маршрута может переместить активный трафик. Проверьте адрес и возможность отката.",
		},
		{
			id: "external-apps",
			group: "Безопасность и обслуживание",
			title: "Внешние приложения и файлы",
			location: "Внешние приложения",
			goal: "Проверяйте файлы подключённого приложения и обновляйте их безопасно.",
			steps: [
				"Откройте приложение и проверьте соединение и список файлов.",
				"Перед загрузкой проверьте файл и назначение.",
				"Прочитайте результат операции и проверьте состояние приложения.",
			],
			goodToKnow:
				"Замена конфигурации может изменить внешнюю службу. Сохраните текущую версию.",
		},
		{
			id: "subscriptions",
			group: "Пользователи и доступ",
			title: "Ссылки подписки и форматы клиентов",
			location: "Пользователи → Подписка / настройки Xray",
			goal: "Проверьте результат подписки для нужного клиента.",
			steps: [
				"Скопируйте ссылку пользователя и проверьте его сервис и хосты.",
				"При необходимости проверьте формат клиента и шаблон.",
				"Импортируйте результат в тестовый клиент и проверьте адрес, протокол и срок.",
			],
			goodToKnow:
				"Ссылка подписки даёт доступ к аккаунту. Передавайте её приватно и замените при утечке.",
		},
		{
			id: "roles",
			group: "Пользователи и доступ",
			title: "Роли и права администратора",
			location: "Администраторы → Данные",
			goal: "Ограничьте оператора необходимыми правами и сервисами.",
			steps: [
				"Откройте администратора и проверьте роль и назначенные сервисы.",
				"Включите только необходимые права и проверьте ограничения аккаунта.",
				"Сохраните и убедитесь, какие разделы доступны этой роли.",
			],
			goodToKnow:
				"Полный доступ и управление администраторами имеют широкий эффект. Не используйте их для обычных задач.",
		},
		{
			id: "telegram",
			group: "Безопасность и обслуживание",
			title: "Уведомления Telegram и копии",
			location: "Настройки панели → Telegram",
			goal: "Подключите бота и проверьте уведомления и резервные копии.",
			steps: [
				"Введите токен и адрес назначения в настройках Telegram и сохраните.",
				"Отправьте тестовое сообщение и проверьте, что оно пришло.",
				"Для расписания копий проверьте адрес и результат; отдельно скачайте и проверьте файл.",
			],
			goodToKnow:
				"Токен бота даёт доступ. Не публикуйте его и замените при утечке.",
		},
		{
			id: "xray-logs",
			group: "Диагностика",
			title: "Журналы Xray и проверка соединения",
			location: "Журналы Xray",
			goal: "Найдите ошибки runtime отдельно от действий администраторов.",
			steps: [
				"Откройте журналы и выберите узел и период события.",
				"Сравните записи со здоровьем узла и состоянием пользователя.",
				"После изменения проверьте узел снова и выполните тестовое подключение.",
			],
			goodToKnow:
				"В журнале могут быть IP и идентификаторы. Удалите их перед передачей.",
		},
		{
			id: "database",
			group: "Безопасность и обслуживание",
			title: "Инструменты базы данных",
			location: "phpMyAdmin",
			goal: "Читайте записи базы только для конкретной административной задачи.",
			steps: [
				"Открывайте инструмент из панели только для разрешённой задачи.",
				"Предпочитайте чтение: прямое редактирование обходит правила учёта и синхронизации.",
				"Используйте действия панели или миграции и затем проверьте её состояние.",
			],
			goodToKnow:
				"Прямое изменение записей может нарушить трафик, лимиты, подписки и доступ.",
		},
	],
};

const ui = {
	fa: {
		eyebrow: "راهنمای عملیاتی AntiMage",
		intro:
			"از اولین ورود تا مدیریت کاربران، نودها و API؛ هر درس مسیر دقیق پنل و کاری که باید انجام دهید را نشان می‌دهد.",
		search: "جست‌وجوی درس یا بخش پنل…",
		visual: "نمای شماتیک پنل",
		illustration: "این تصویر راهنمای مسیر است، نه اسکرین‌شات Production.",
		lessons: "درس‌ها",
		step: "مرحله",
		previous: "مرحله قبل",
		next: "مرحله بعد",
		open: "بازکردن صفحه در پنل",
		api: "مرجع API",
		all: "همه درس‌ها",
		progress: "پیشرفت راهنما",
		click: "مسیر در پنل",
		safe: "نکته ایمنی",
		noResults: "درسی با این جست‌وجو پیدا نشد.",
	},
	en: {
		eyebrow: "AntiMage field guide",
		intro:
			"From first sign-in to users, nodes, and API. Every lesson shows where to go and what to do.",
		search: "Search lessons or panel sections…",
		visual: "Panel map illustration",
		illustration:
			"This is a navigation illustration, not a Production screenshot.",
		lessons: "Lessons",
		step: "Step",
		previous: "Previous step",
		next: "Next step",
		open: "Open this panel page",
		api: "API reference",
		all: "All lessons",
		progress: "Guide progress",
		click: "Panel path",
		safe: "Safety note",
		noResults: "No lessons match this search.",
	},
	ru: {
		eyebrow: "Практическое руководство AntiMage",
		intro:
			"От первого входа до пользователей, узлов и API: каждый урок показывает путь и нужные действия.",
		search: "Поиск урока или раздела…",
		visual: "Схема панели",
		illustration: "Это схема навигации, а не снимок Production.",
		lessons: "Уроки",
		step: "Шаг",
		previous: "Предыдущий шаг",
		next: "Следующий шаг",
		open: "Открыть раздел панели",
		api: "Справочник API",
		all: "Все уроки",
		progress: "Прогресс руководства",
		click: "Путь в панели",
		safe: "Важно для безопасности",
		noResults: "Уроки не найдены.",
	},
};

const routes: Record<string, string> = {
	overview: "/",
	users: "/users",
	"create-user": "/users",
	lifecycle: "/users",
	services: "/services",
	inbounds: "/hosts",
	nodes: "/node-settings",
	usage: "/usage",
	backup: "/",
	admins: "/admins",
	settings: "/settings",
	integrations: "/settings",
	logs: "/recent-actions",
	insights: "/access-insights",
	api: "/api-docs",
	account: "/myaccount",
	bulk: "/bulk-actions",
	haproxy: "/haproxy",
	"external-apps": "/external-apps",
	subscriptions: "/users",
	roles: "/admins",
	telegram: "/settings",
	"xray-logs": "/xray-logs",
	database: "/phpmyadmin",
};

const PanelIllustration = ({
	lesson,
	language,
}: {
	lesson: Lesson;
	language: Language;
}) => {
	const labels =
		language === "fa"
			? [
					"داشبورد",
					"کاربران",
					"عملیات گروهی",
					"مدیران",
					"نودها",
					"سرویس‌ها",
					"هاست‌ها",
					"HAProxy",
					"مصرف",
					"لاگ‌ها",
					"تنظیمات",
					"برنامه‌های خارجی",
					"پایگاه داده",
					"API و مستندات",
				]
			: language === "ru"
				? [
						"Панель",
						"Пользователи",
						"Групповые действия",
						"Администраторы",
						"Узлы",
						"Сервисы",
						"Хосты",
						"HAProxy",
						"Трафик",
						"Журналы",
						"Настройки",
						"Внешние приложения",
						"База данных",
						"API и документы",
					]
				: [
						"Dashboard",
						"Users",
						"Bulk actions",
						"Admins",
						"Nodes",
						"Services",
						"Hosts",
						"HAProxy",
						"Usage",
						"Logs",
						"Settings",
						"External apps",
						"Database",
						"API & docs",
					];
	const target =
		lesson.id === "overview"
			? 0
			: lesson.id === "users" ||
					lesson.id === "create-user" ||
					lesson.id === "lifecycle" ||
					lesson.id === "subscriptions"
				? 1
				: lesson.id === "bulk"
					? 2
					: lesson.id === "admins" || lesson.id === "roles"
						? 3
						: lesson.id === "nodes"
							? 4
							: lesson.id === "services"
								? 5
								: lesson.id === "inbounds"
									? 6
									: lesson.id === "haproxy"
										? 7
										: lesson.id === "usage" || lesson.id === "insights"
											? 8
											: lesson.id === "logs" || lesson.id === "xray-logs"
												? 9
												: lesson.id === "api"
													? 13
													: lesson.id === "external-apps"
														? 11
														: lesson.id === "database"
															? 12
															: lesson.id === "account" ||
																	lesson.id === "settings" ||
																	lesson.id === "integrations" ||
																	lesson.id === "telegram"
																? 10
																: 0;
	return (
		<Box
			aria-label={`${lesson.location} ${language === "fa" ? "نمای شماتیک" : language === "ru" ? "схема" : "illustration"}`}
			border="1px solid"
			borderColor="whiteAlpha.200"
			borderRadius="xl"
			bg="#080e18"
			p={{ base: 3, md: 5 }}
			overflow="hidden"
		>
			<Flex gap={2} mb={4} align="center">
				<Box w="9px" h="9px" borderRadius="full" bg="#f04d9b" />
				<Box w="9px" h="9px" borderRadius="full" bg="#e9b35a" />
				<Box w="9px" h="9px" borderRadius="full" bg="#55c99b" />
				<Text ms={2} color="whiteAlpha.600" fontSize="xs">
					panel.antimage
				</Text>
			</Flex>
			<Grid
				templateColumns={{ base: "105px 1fr", md: "150px 1fr" }}
				gap={3}
				minH="235px"
			>
				<VStack
					align="stretch"
					spacing={1}
					borderInlineEnd="1px solid"
					borderColor="whiteAlpha.200"
					pe={2}
				>
					{labels.map((label, index) => (
						<Flex
							key={label}
							align="center"
							gap={2}
							px={2}
							py={2}
							borderRadius="md"
							bg={index === target ? "#321a31" : "transparent"}
							color={index === target ? "#ff80bc" : "whiteAlpha.700"}
							fontSize={{ base: "9px", md: "xs" }}
						>
							<Box
								w="6px"
								h="6px"
								borderRadius="full"
								bg={index === target ? "#fa51a1" : "whiteAlpha.400"}
							/>
							<Text noOfLines={1}>{label}</Text>
						</Flex>
					))}
				</VStack>
				<VStack align="stretch" spacing={3}>
					<Flex justify="space-between" align="center">
						<Box>
							<Text color="whiteAlpha.500" fontSize="xs">
								AntiMage / {lesson.location}
							</Text>
							<Heading size={{ base: "xs", md: "sm" }} color="white" mt={1}>
								{lesson.title}
							</Heading>
						</Box>
						<Badge colorScheme="pink" variant="subtle">
							{language === "fa"
								? "محل کار"
								: language === "ru"
									? "Раздел"
									: "SECTION"}
						</Badge>
					</Flex>
					<SimpleGrid columns={{ base: 1, md: 2 }} spacing={2}>
						<Box
							bg="#111b29"
							border="1px solid"
							borderColor="whiteAlpha.200"
							borderRadius="lg"
							p={3}
						>
							<Text fontSize="xs" color="whiteAlpha.600">
								{language === "fa"
									? "هدف این درس"
									: language === "ru"
										? "Цель урока"
										: "LESSON GOAL"}
							</Text>
							<Text fontSize="xs" color="white" mt={2}>
								{lesson.goal}
							</Text>
						</Box>
						<Box
							bg="#111b29"
							border="1px solid"
							borderColor={target === 7 ? "#f04d9b" : "whiteAlpha.200"}
							borderRadius="lg"
							p={3}
						>
							<Text fontSize="xs" color="whiteAlpha.600">
								{language === "fa"
									? "نقطه شروع"
									: language === "ru"
										? "Начните здесь"
										: "START HERE"}
							</Text>
							<Text fontSize="sm" color="#ff80bc" mt={2}>
								{lesson.location}
							</Text>
						</Box>
					</SimpleGrid>
					<Box
						bg="#111b29"
						border="1px solid"
						borderColor="whiteAlpha.200"
						borderRadius="lg"
						p={3}
					>
						<Text fontSize="xs" color="whiteAlpha.600" mb={2}>
							{language === "fa"
								? "روند انجام کار"
								: language === "ru"
									? "Порядок действий"
									: "WORKFLOW"}
						</Text>
						<Flex align="center" gap={2} wrap="wrap">
							{lesson.steps.map((_, index) => (
								<Flex key={`${lesson.id}-${index}`} align="center" gap={2}>
									<Badge
										variant={index === 0 ? "solid" : "outline"}
										colorScheme={index === 0 ? "pink" : "gray"}
										borderRadius="full"
									>
										{index + 1}
									</Badge>
									<Box
										w={{ base: "22px", md: "40px" }}
										h="1px"
										bg="whiteAlpha.300"
									/>
								</Flex>
							))}
						</Flex>
						<Text color="whiteAlpha.700" fontSize="xs" mt={3}>
							{lesson.steps[0]}
						</Text>
					</Box>
				</VStack>
			</Grid>
			<Text color="whiteAlpha.500" fontSize="xs" mt={3}>
				{ui[language].illustration}
			</Text>
		</Box>
	);
};

export const TutorialsPage = () => {
	const { i18n } = useTranslation();
	const apiHref = useHref("/guide/api");
	const [searchParams] = useSearchParams();
	const language = (i18n.resolvedLanguage || i18n.language || "en").startsWith(
		"fa",
	)
		? "fa"
		: (i18n.resolvedLanguage || i18n.language || "en").startsWith("ru")
			? "ru"
			: "en";
	const copy = ui[language];
	const catalog = lessons[language];
	const requestedTopic = searchParams.get("topic") || "";
	const [selectedId, setSelectedId] = useState(() =>
		catalog.some((lesson) => lesson.id === requestedTopic)
			? requestedTopic
			: "overview",
	);
	const [stepIndex, setStepIndex] = useState(0);
	const [query, setQuery] = useState("");
	const [group, setGroup] = useState("");
	const selected =
		catalog.find((lesson) => lesson.id === selectedId) || catalog[0];
	const targetHref = useHref(routes[selected.id] || "/");
	const filtered = useMemo(
		() =>
			catalog.filter(
				(lesson) =>
					`${lesson.title} ${lesson.group} ${lesson.location}`
						.toLowerCase()
						.includes(query.toLowerCase()) &&
					(!group || lesson.group === group),
			),
		[catalog, group, query],
	);
	const groups = [...new Set(catalog.map((lesson) => lesson.group))];
	const lessonIndex = catalog.findIndex((lesson) => lesson.id === selected.id);
	const progress = Math.round(((lessonIndex + 1) / catalog.length) * 100);
	const rtl = language === "fa";
	const selectLesson = (id: string) => {
		setSelectedId(id);
		setStepIndex(0);
	};
	const moveLesson = (direction: -1 | 1) => {
		const next =
			catalog[
				Math.min(catalog.length - 1, Math.max(0, lessonIndex + direction))
			];
		if (next) selectLesson(next.id);
	};
	return (
		<Box minH="100vh" bg="#080b12" color="#e8edf7" dir={rtl ? "rtl" : "ltr"}>
			<Container maxW="1500px" py={{ base: 4, md: 8 }} px={{ base: 4, md: 8 }}>
				<Flex
					justify="space-between"
					align="center"
					gap={4}
					borderBottom="1px solid"
					borderColor="whiteAlpha.200"
					pb={5}
					mb={8}
					wrap="wrap"
				>
					<HStack spacing={3}>
						<Box bg="#28162a" p={2} borderRadius="lg">
							<BookOpenIcon width={24} color="#f05aa3" />
						</Box>
						<Box>
							<Text color="#f05aa3" fontSize="xs" letterSpacing=".12em">
								{copy.eyebrow}
							</Text>
							<Heading size="md" color="white">
								AntiMage
							</Heading>
						</Box>
					</HStack>
					<HStack>
						<Button
							as="a"
							href={apiHref}
							size="sm"
							variant="outline"
							borderColor="whiteAlpha.300"
							leftIcon={<ShieldCheckIcon width={16} />}
						>
							{copy.api}
						</Button>
						<HStack color="whiteAlpha.700" fontSize="xs" spacing={2}>
							<Text>
								{language === "fa"
									? "زبان:"
									: language === "ru"
										? "Язык:"
										: "Language:"}
							</Text>
							{(["fa", "en", "ru"] as const).map((code) => (
								<Button
									key={code}
									size="xs"
									variant={language === code ? "solid" : "ghost"}
									colorScheme={language === code ? "pink" : "gray"}
									onClick={() => void i18n.changeLanguage(code)}
								>
									{code === "fa"
										? "فارسی"
										: code === "ru"
											? "Русский"
											: "English"}
								</Button>
							))}
						</HStack>
					</HStack>
				</Flex>
				<Grid
					templateColumns={{ base: "1fr", lg: "320px minmax(0, 1fr)" }}
					gap={{ base: 6, lg: 8 }}
					alignItems="start"
				>
					<GridItem>
						<Box
							position={{ lg: "sticky" }}
							top="20px"
							border="1px solid"
							borderColor="whiteAlpha.200"
							bg="#0d1420"
							borderRadius="2xl"
							p={4}
						>
							<HStack mb={3}>
								<PlayCircleIcon width={20} color="#f05aa3" />
								<Heading size="sm">{copy.lessons}</Heading>
							</HStack>
							<InputGroup size="sm" mb={3}>
								<InputLeftElement pointerEvents="none">
									<MagnifyingGlassIcon width={16} />
								</InputLeftElement>
								<Input
									value={query}
									onChange={(event) => setQuery(event.target.value)}
									placeholder={copy.search}
									bg="#090f19"
									borderColor="whiteAlpha.300"
								/>
							</InputGroup>
							<Flex gap={2} wrap="wrap" mb={4}>
								<Button
									size="xs"
									onClick={() => setGroup("")}
									colorScheme={!group ? "pink" : "gray"}
									variant={!group ? "solid" : "outline"}
								>
									{copy.all}
								</Button>
								{groups.map((name) => (
									<Button
										key={name}
										size="xs"
										onClick={() => setGroup(group === name ? "" : name)}
										colorScheme={group === name ? "pink" : "gray"}
										variant={group === name ? "solid" : "outline"}
									>
										{name}
									</Button>
								))}
							</Flex>
							<Progress
								value={progress}
								size="xs"
								colorScheme="pink"
								borderRadius="full"
								mb={2}
							/>
							<Text fontSize="xs" color="whiteAlpha.600" mb={3}>
								{copy.progress}: {progress}%
							</Text>
							<VStack
								align="stretch"
								spacing={1}
								maxH={{ base: "300px", lg: "calc(100vh - 350px)" }}
								overflowY="auto"
							>
								{filtered.map((lesson) => (
									<Button
										key={lesson.id}
										onClick={() => selectLesson(lesson.id)}
										justifyContent="flex-start"
										whiteSpace="normal"
										h="auto"
										minH="48px"
										py={2}
										px={3}
										color={
											lesson.id === selected.id ? "#ff87be" : "whiteAlpha.800"
										}
										bg={lesson.id === selected.id ? "#2d1a30" : "transparent"}
										_hover={{ bg: "whiteAlpha.100" }}
									>
										<Stack align="start" spacing={0}>
											<Text fontSize="sm">{lesson.title}</Text>
											<Text fontSize="xs" color="whiteAlpha.500">
												{lesson.group}
											</Text>
										</Stack>
									</Button>
								))}
								{filtered.length === 0 && (
									<Text color="whiteAlpha.600" p={3} fontSize="sm">
										{copy.noResults}
									</Text>
								)}
							</VStack>
						</Box>
					</GridItem>
					<GridItem>
						<VStack align="stretch" spacing={5}>
							<Box>
								<Badge colorScheme="pink" mb={3}>
									{selected.group}
								</Badge>
								<Heading size={{ base: "lg", md: "2xl" }} color="white" mb={3}>
									{selected.title}
								</Heading>
								<Text
									color="whiteAlpha.700"
									fontSize={{ base: "md", md: "lg" }}
									maxW="850px"
								>
									{selected.goal}
								</Text>
							</Box>
							<PanelIllustration lesson={selected} language={language} />
							<SimpleGrid columns={{ base: 1, md: 2 }} spacing={5}>
								<Box
									border="1px solid"
									borderColor="whiteAlpha.200"
									borderRadius="xl"
									p={{ base: 4, md: 6 }}
									bg="#0d1420"
								>
									<HStack mb={4}>
										<Badge colorScheme="pink">
											{copy.step} {stepIndex + 1} / 3
										</Badge>
										<Text color="whiteAlpha.500" fontSize="sm">
											{selected.location}
										</Text>
									</HStack>
									<Heading size="md" mb={4}>
										{selected.title}
									</Heading>
									<Text fontSize={{ base: "md", md: "lg" }} lineHeight="1.9">
										{selected.steps[stepIndex]}
									</Text>
									<HStack mt={6} justify="space-between">
										<Button
											size="sm"
											variant="ghost"
											leftIcon={
												rtl ? (
													<ArrowRightIcon width={16} />
												) : (
													<ArrowLeftIcon width={16} />
												)
											}
											isDisabled={stepIndex === 0}
											onClick={() => setStepIndex(Math.max(0, stepIndex - 1))}
										>
											{copy.previous}
										</Button>
										{stepIndex < 2 ? (
											<Button
												size="sm"
												colorScheme="pink"
												rightIcon={
													rtl ? (
														<ArrowLeftIcon width={16} />
													) : (
														<ArrowRightIcon width={16} />
													)
												}
												onClick={() => setStepIndex(stepIndex + 1)}
											>
												{copy.next}
											</Button>
										) : (
											<Button
												size="sm"
												colorScheme="green"
												leftIcon={<CheckCircleIcon width={16} />}
												onClick={() => moveLesson(1)}
												isDisabled={lessonIndex === catalog.length - 1}
											>
												{language === "fa"
													? "پایان درس"
													: language === "ru"
														? "Завершить урок"
														: "Finish lesson"}
											</Button>
										)}
									</HStack>
								</Box>
								<Stack spacing={4}>
									<Box
										border="1px solid"
										borderColor="whiteAlpha.200"
										borderRadius="xl"
										p={5}
										bg="#0d1420"
									>
										<Text color="#ff87be" fontWeight="bold" mb={2}>
											{copy.click}
										</Text>
										<Text color="whiteAlpha.700" fontSize="sm">
											{selected.location}
										</Text>
										<Button
											as="a"
											href={targetHref}
											size="sm"
											mt={4}
											variant="outline"
											borderColor="#f05aa3"
										>
											{copy.open}
										</Button>
									</Box>
									<Alert
										status="info"
										borderRadius="xl"
										bg="#102031"
										color="whiteAlpha.900"
										alignItems="flex-start"
									>
										<AlertIcon color="#57b4e5" />
										<Box>
											<Text fontWeight="bold" mb={1}>
												{copy.safe}
											</Text>
											<Text fontSize="sm" lineHeight="1.8">
												{selected.goodToKnow}
											</Text>
										</Box>
									</Alert>
								</Stack>
							</SimpleGrid>
							<Flex
								justify="space-between"
								borderTop="1px solid"
								borderColor="whiteAlpha.200"
								pt={4}
							>
								<Button
									variant="ghost"
									onClick={() => moveLesson(-1)}
									isDisabled={lessonIndex === 0}
								>
									{language === "fa"
										? "درس قبلی"
										: language === "ru"
											? "Предыдущий урок"
											: "Previous lesson"}
								</Button>
								<Button
									colorScheme="pink"
									onClick={() => moveLesson(1)}
									isDisabled={lessonIndex === catalog.length - 1}
								>
									{language === "fa"
										? "درس بعدی"
										: language === "ru"
											? "Следующий урок"
											: "Next lesson"}
								</Button>
							</Flex>
						</VStack>
					</GridItem>
				</Grid>
			</Container>
		</Box>
	);
};
