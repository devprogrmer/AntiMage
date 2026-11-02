package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	dashboardapp "github.com/antimage/antimage/internal/app/dashboard"
	"github.com/antimage/antimage/internal/app/shop"
	telegrambot "github.com/antimage/antimage/internal/app/telegram/bot"
	userapp "github.com/antimage/antimage/internal/app/user"
)

// botSettingsSource adapts the Telegram repository to the bot's SettingsSource.
type botSettingsSource struct {
	server *Server
}

func (b botSettingsSource) BotSettings(ctx context.Context) (telegrambot.Settings, error) {
	settings, err := b.server.telegramRepo.Settings(ctx)
	if err != nil {
		return telegrambot.Settings{}, err
	}
	token := ""
	if settings.APIToken != nil {
		token = strings.TrimSpace(*settings.APIToken)
	}
	proxy := ""
	if settings.ProxyURL != nil {
		proxy = strings.TrimSpace(*settings.ProxyURL)
	}
	return telegrambot.Settings{
		Enabled:      settings.UseTelegram,
		Token:        token,
		ProxyURL:     proxy,
		AdminChatIDs: settings.AdminChatIDs,
	}, nil
}

// botAuthorizer resolves the active full-access admin bot mutations run as.
type botAuthorizer struct {
	server *Server
}

func (b botAuthorizer) Actor(ctx context.Context) (telegrambot.Actor, bool) {
	admin, ok, err := b.server.adminRepo.FirstFullAccessAdmin(ctx)
	if err != nil || !ok {
		return telegrambot.Actor{}, false
	}
	return telegrambot.Actor{Username: admin.Username, Admin: admin}, true
}

// botUserService adapts the user service to the bot's UserService, enforcing the
// same Go service permission/limit core as the HTTP API.
type botUserService struct {
	server *Server
}

func (b botUserService) Get(ctx context.Context, username string) (telegrambot.UserView, error) {
	detail, err := b.server.userService.UserGet(ctx, userapp.UserGetRequest{
		Username: username,
		Admin: userapp.AdminContext{
			Username:       "telegram-bot",
			Role:           string(adminapp.RoleFullAccess),
			CanViewTraffic: true,
			CanSortTraffic: true,
		},
	})
	if err != nil {
		return telegrambot.UserView{}, err
	}
	view := telegrambot.UserView{
		Username:        detail.Username,
		TelegramID:      detail.TelegramID,
		Status:          detail.Status,
		UsedTraffic:     detail.UsedTraffic,
		DataLimit:       detail.DataLimit,
		Expire:          detail.Expire,
		OnlineAt:        detail.OnlineAt,
		SubUpdatedAt:    detail.SubUpdatedAt,
		SubscriptionURL: detail.SubscriptionURL,
		Links:           detail.Links,
	}
	if detail.Note != nil {
		view.Note = *detail.Note
	}
	if detail.AdminUsername != nil {
		view.OwnerAdmin = *detail.AdminUsername
	}
	return view, nil
}

func (b botUserService) ListByTelegramID(ctx context.Context, telegramID int64) ([]telegrambot.UserView, error) {
	if telegramID <= 0 {
		return nil, nil
	}
	id := strconv.FormatInt(telegramID, 10)
	rows, err := b.server.db.QueryContext(ctx, `SELECT username FROM users WHERE telegram_id = ? AND status != 'deleted' ORDER BY username LIMIT 20`, id)
	if err != nil {
		return nil, err
	}
	var usernames []string
	for rows.Next() {
		var username string
		if err := rows.Scan(&username); err != nil {
			rows.Close()
			return nil, err
		}
		usernames = append(usernames, username)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	views := make([]telegrambot.UserView, 0, len(usernames))
	for _, username := range usernames {
		view, err := b.Get(ctx, username)
		if err != nil {
			return nil, err
		}
		if view.TelegramID != nil && *view.TelegramID == id {
			views = append(views, view)
		}
	}
	return views, nil
}

func (b botUserService) actorAdmin(actor telegrambot.Actor) (adminapp.Admin, error) {
	admin, ok := actor.Admin.(adminapp.Admin)
	if !ok {
		return adminapp.Admin{}, fmt.Errorf("invalid admin actor")
	}
	return admin, nil
}

func (b botUserService) Delete(ctx context.Context, actor telegrambot.Actor, username string) error {
	admin, err := b.actorAdmin(actor)
	if err != nil {
		return err
	}
	_, err = b.server.userService.DeleteUser(ctx, admin, username)
	return err
}

func (b botUserService) Reset(ctx context.Context, actor telegrambot.Actor, username string) error {
	admin, err := b.actorAdmin(actor)
	if err != nil {
		return err
	}
	_, err = b.server.userService.ResetUser(ctx, admin, username)
	return err
}

func (b botUserService) RevokeSubscription(ctx context.Context, actor telegrambot.Actor, username string) error {
	admin, err := b.actorAdmin(actor)
	if err != nil {
		return err
	}
	_, err = b.server.userService.RevokeUserSubscription(ctx, admin, username)
	return err
}

func (b botUserService) SetStatus(ctx context.Context, actor telegrambot.Actor, username string, status string) error {
	return b.update(ctx, actor, username, map[string]any{"status": status})
}

func (b botUserService) SetNote(ctx context.Context, actor telegrambot.Actor, username string, note string) error {
	return b.update(ctx, actor, username, map[string]any{"note": note})
}

func (b botUserService) update(ctx context.Context, actor telegrambot.Actor, username string, fields map[string]any) error {
	admin, err := b.actorAdmin(actor)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = b.server.userService.UpdateUser(ctx, admin, username, raw)
	return err
}

type botShopService struct{ server *Server }

func (b botShopService) stores() (shop.Orders, shop.Wallet) {
	return shop.Orders{DB: b.server.db}, shop.Wallet{DB: b.server.db, Dialect: b.server.dialect}
}

func (b botShopService) Balance(ctx context.Context, telegramID int64) (int64, error) {
	_, wallet := b.stores()
	return wallet.Balance(ctx, telegramID)
}

func (b botShopService) Products(ctx context.Context) ([]shop.Product, error) {
	orders, _ := b.stores()
	return orders.ListProducts(ctx)
}

func (b botShopService) Orders(ctx context.Context, telegramID int64) ([]shop.Order, error) {
	orders, _ := b.stores()
	return orders.ListCustomerOrders(ctx, telegramID)
}

func (b botShopService) Place(ctx context.Context, telegramID, productID int64, requestKey string) (shop.Order, error) {
	orders, _ := b.stores()
	return orders.Place(ctx, telegramID, productID, requestKey)
}

func (b botShopService) AdminCredit(ctx context.Context, telegramID, amount int64, reference string) (int64, error) {
	_, wallet := b.stores()
	return wallet.Credit(ctx, telegramID, amount, reference)
}

func (b botShopService) AddProduct(ctx context.Context, product shop.Product, requestKey string) (shop.Product, error) {
	if err := b.serviceHasActiveHost(ctx, product.ServiceID); err != nil {
		return shop.Product{}, err
	}
	orders, _ := b.stores()
	return orders.CreateProduct(ctx, product, requestKey)
}

func (b botShopService) DisableProduct(ctx context.Context, productID int64) error {
	orders, _ := b.stores()
	return orders.DisableProduct(ctx, productID)
}

func (b botShopService) serviceHasActiveHost(ctx context.Context, serviceID int64) error {
	var exists int
	err := b.server.db.QueryRowContext(ctx, `SELECT 1 FROM service_hosts sh JOIN hosts h ON h.id = sh.host_id WHERE sh.service_id = ? AND COALESCE(h.is_disabled, 0) = 0 LIMIT 1`, serviceID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("service has no active host")
	}
	return err
}

func (b botShopService) PayAndProvision(ctx context.Context, telegramID int64, orderID string) (string, error) {
	orders, wallet := b.stores()
	var serviceID int64
	var owner int64
	var status string
	if err := b.server.db.QueryRowContext(ctx, `SELECT telegram_id, service_id, status FROM shop_orders WHERE id = ?`, orderID).Scan(&owner, &serviceID, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", shop.ErrOrderUnavailable
		}
		return "", err
	}
	if owner != telegramID {
		return "", shop.ErrOrderUnavailable
	}
	if status == "pending" {
		if err := b.serviceHasActiveHost(ctx, serviceID); err != nil {
			return "", err
		}
	}
	if _, err := orders.Pay(ctx, telegramID, orderID, wallet); err != nil {
		return "", err
	}
	return orders.Provision(ctx, orderID, botAccountProvisioner{server: b.server})
}

type botAccountProvisioner struct{ server *Server }

func (b botAccountProvisioner) Lookup(ctx context.Context, username string) (shop.Account, bool, error) {
	var account shop.Account
	var telegramID, serviceID, limit, duration sql.NullInt64
	var note sql.NullString
	err := b.server.db.QueryRowContext(ctx, `SELECT username, telegram_id, service_id, data_limit, on_hold_expire_duration, note FROM users WHERE username = ? AND status != 'deleted'`, username).
		Scan(&account.Username, &telegramID, &serviceID, &limit, &duration, &note)
	if errors.Is(err, sql.ErrNoRows) {
		return shop.Account{}, false, nil
	}
	if err != nil {
		return shop.Account{}, false, err
	}
	account.TelegramID = telegramID.Int64
	account.ServiceID = serviceID.Int64
	account.DataLimitBytes = limit.Int64
	account.HoldDurationSec = duration.Int64
	account.OrderMarker = note.String
	return account, true, nil
}

func (b botAccountProvisioner) Create(ctx context.Context, account shop.Account) error {
	admin, ok, err := b.server.adminRepo.FirstFullAccessAdmin(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no active full-access admin for order delivery")
	}
	raw, err := json.Marshal(map[string]any{
		"username": account.Username, "service_id": account.ServiceID,
		"telegram_id": strconv.FormatInt(account.TelegramID, 10),
		"data_limit":  account.DataLimitBytes,
		"status":      "on_hold", "on_hold_expire_duration": account.HoldDurationSec,
		"note": account.OrderMarker,
	})
	if err != nil {
		return err
	}
	_, err = b.server.userService.CreateUser(ctx, admin, raw)
	return err
}

// botSystemService adapts the system stats service to the bot's SystemService.
type botSystemService struct {
	server *Server
}

func (b botSystemService) Info(ctx context.Context) (telegrambot.SystemInfo, error) {
	stats, err := b.server.systemStatsService().Stats(ctx, dashboardapp.AdminContext{
		Username: "telegram-bot",
		Role:     string(adminapp.RoleFullAccess),
	})
	if err != nil {
		return telegrambot.SystemInfo{}, err
	}
	return telegrambot.SystemInfo{
		Version:     stats.Version,
		CPUPercent:  stats.CPUUsage,
		MemUsed:     stats.Memory.Current,
		MemTotal:    stats.Memory.Total,
		TotalUsers:  stats.TotalUser,
		ActiveUsers: stats.UsersActive,
		OnlineUsers: stats.OnlineUsers,
	}, nil
}

// runTelegramBot starts the long-polling Telegram admin bot. It idles until
// Telegram is enabled in the dashboard.
func (s *Server) runTelegramBot(ctx context.Context) {
	b := telegrambot.New(telegrambot.Options{
		APIBase:    s.cfg.TelegramAPIBase,
		Settings:   botSettingsSource{server: s},
		Authorizer: botAuthorizer{server: s},
		Users:      botUserService{server: s},
		Customers:  botUserService{server: s},
		Shop:       botShopService{server: s},
		System:     botSystemService{server: s},
		DB:         s.db,
	})
	b.Run(ctx)
}
