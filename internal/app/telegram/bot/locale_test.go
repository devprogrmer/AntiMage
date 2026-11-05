package bot

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestLanguageSelectionPersistsAndMenuIsLocalized(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE telegram_bot_languages (telegram_id BIGINT PRIMARY KEY, language VARCHAR(2) NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	state := newStateStore(db)
	ctx := context.Background()
	if got := state.language(ctx, 123, "fa-IR"); got != "fa" {
		t.Fatalf("profile language: %q", got)
	}
	if err := state.setLanguage(ctx, 123, "ru"); err != nil {
		t.Fatal(err)
	}
	if got := newStateStore(db).language(ctx, 123, "fa-IR"); got != "ru" {
		t.Fatalf("saved language: %q", got)
	}
	if err := state.setLanguage(ctx, 123, "en"); err != nil {
		t.Fatal(err)
	}
	if err := state.setLanguage(ctx, 123, "en"); err != nil {
		t.Fatalf("setting same language twice: %v", err)
	}
	if got := state.language(ctx, 123, "ru"); got != "en" {
		t.Fatalf("updated language: %q", got)
	}
	menu := menuForLanguage(false, "fa", "https://panel.example/telegram-app")
	if menu.InlineKeyboard[0][0].Text != "🛒 طرح‌ها" {
		t.Fatalf("Persian menu: %+v", menu.InlineKeyboard[0])
	}
	if menu.InlineKeyboard[len(menu.InlineKeyboard)-2][0].WebApp == nil {
		t.Fatal("localized menu lost Mini App button")
	}
	menu = menuForLanguage(false, "ru", "")
	if menu.InlineKeyboard[0][0].Text != "🛒 Тарифы" {
		t.Fatalf("Russian menu: %+v", menu.InlineKeyboard[0])
	}
}
