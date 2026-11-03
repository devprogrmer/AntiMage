// Package bot implements the AntiMage Telegram admin bot using getUpdates
// long-polling, matching the transport of the legacy Python bot. It is
// decoupled from the concrete Go services through the interfaces below; the API
// layer wires real implementations so bot commands reuse the same Go services as
// the HTTP API instead of touching the database directly.
package bot

import (
	"context"

	"github.com/antimage/antimage/internal/app/shop"
	"github.com/antimage/antimage/internal/app/support"
)

// Update mirrors the subset of the Telegram getUpdates response the bot uses.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Message struct {
	MessageID int64       `json:"message_id"`
	From      *User       `json:"from"`
	Chat      Chat        `json:"chat"`
	Text      string      `json:"text"`
	Caption   string      `json:"caption"`
	Photo     []PhotoSize `json:"photo"`
}

type PhotoSize struct {
	FileID string `json:"file_id"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type User struct {
	ID int64 `json:"id"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// InlineKeyboard is a Telegram inline_keyboard markup.
type InlineKeyboard struct {
	InlineKeyboard [][]InlineButton `json:"inline_keyboard"`
}

type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// Settings is the snapshot the bot needs each poll cycle.
type Settings struct {
	Enabled      bool
	Token        string
	ProxyURL     string
	AdminChatIDs []int64
}

// SettingsSource provides the current Telegram settings.
type SettingsSource interface {
	BotSettings(ctx context.Context) (Settings, error)
}

// Actor identifies the admin a bot action runs as. Admin is carried opaquely so
// the bot package does not depend on the admin package; the API adapter casts it
// back to its concrete admin type.
type Actor struct {
	Username string
	Admin    any
}

// Authorizer resolves the admin actor that authorized bot mutations run as.
// Access control itself is the allowlist in Settings.AdminChatIDs; this returns
// the panel admin (typically a full-access admin) used for service calls.
type Authorizer interface {
	Actor(ctx context.Context) (Actor, bool)
}

// UserView is the data the bot renders for a user.
type UserView struct {
	Username        string
	TelegramID      *string
	Status          string
	UsedTraffic     int64
	DataLimit       *int64
	Expire          *int64
	OnlineAt        *string
	SubUpdatedAt    *string
	Note            string
	OwnerAdmin      string
	SubscriptionURL string
	Links           []string
}

// CustomerService looks up only accounts explicitly linked to a Telegram ID.
type CustomerService interface {
	ListByTelegramID(ctx context.Context, telegramID int64) ([]UserView, error)
}

type NodeView struct {
	ID              int64
	Name            string
	Status          string
	AgentStatus     string
	XrayStatus      string
	AgentVersion    string
	XrayVersion     string
	CPUPercent      float64
	MemoryPercent   float64
	AppliedRevision uint64
	DesiredRevision uint64
}

type NodesService interface {
	List(ctx context.Context) ([]NodeView, error)
	Get(ctx context.Context, id int64) (NodeView, error)
}

type ShopService interface {
	Balance(ctx context.Context, telegramID int64) (int64, error)
	Products(ctx context.Context) ([]shop.Product, error)
	Orders(ctx context.Context, telegramID int64) ([]shop.Order, error)
	Place(ctx context.Context, telegramID, productID int64, requestKey string) (shop.Order, error)
	PayAndProvision(ctx context.Context, telegramID int64, orderID string) (string, error)
	AdminCredit(ctx context.Context, telegramID, amount int64, reference string) (int64, error)
	AddProduct(ctx context.Context, product shop.Product, requestKey string) (shop.Product, error)
	DisableProduct(ctx context.Context, productID int64) error
}

type SupportService interface {
	Create(ctx context.Context, telegramID int64, content, reference string) (support.Ticket, error)
	List(ctx context.Context, telegramID int64, admin bool) ([]support.Ticket, error)
	Get(ctx context.Context, ticketID string, telegramID int64, admin bool) (support.Ticket, error)
	Reply(ctx context.Context, ticketID, content, reference string) (support.Ticket, error)
}

type TopupService interface {
	Get(ctx context.Context, id string) (shop.Topup, error)
	Instructions(ctx context.Context) (string, error)
	SetInstructions(ctx context.Context, instructions string) error
	Create(ctx context.Context, telegramID, amount int64, requestKey string) (shop.Topup, error)
	SubmitReceipt(ctx context.Context, telegramID int64, topupID, fileID string) (shop.Topup, error)
	List(ctx context.Context, telegramID int64, adminPending bool) ([]shop.Topup, error)
	Approve(ctx context.Context, topupID string, adminID int64) (shop.Topup, error)
	Reject(ctx context.Context, topupID string, adminID int64) (shop.Topup, error)
}

// UserService exposes the user operations the bot needs. Implementations must
// enforce the same permissions/limits as the HTTP API.
type UserService interface {
	Get(ctx context.Context, username string) (UserView, error)
	Delete(ctx context.Context, actor Actor, username string) error
	Reset(ctx context.Context, actor Actor, username string) error
	RevokeSubscription(ctx context.Context, actor Actor, username string) error
	SetStatus(ctx context.Context, actor Actor, username string, status string) error
	SetNote(ctx context.Context, actor Actor, username string, note string) error
}

// SystemInfo is the data the bot renders for the system status command.
type SystemInfo struct {
	Version     string
	CPUPercent  float64
	MemUsed     int64
	MemTotal    int64
	TotalUsers  int64
	ActiveUsers int64
	OnlineUsers int64
}

// SystemService exposes read-only system information for the bot.
type SystemService interface {
	Info(ctx context.Context) (SystemInfo, error)
}
