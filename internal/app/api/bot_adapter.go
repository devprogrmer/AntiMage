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
	"github.com/antimage/antimage/internal/app/nodecontroller"
	"github.com/antimage/antimage/internal/app/shop"
	"github.com/antimage/antimage/internal/app/support"
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
	miniAppURL := b.server.cfg.TelegramMiniAppURL
	if settings.MiniAppURL != nil {
		miniAppURL = strings.TrimSpace(*settings.MiniAppURL)
	}
	return telegrambot.Settings{
		Enabled:      settings.UseTelegram,
		Token:        token,
		ProxyURL:     proxy,
		MiniAppURL:   miniAppURL,
		AdminChatIDs: settings.AdminChatIDs,
	}, nil
}

// botAuthorizer resolves the active full-access admin bot mutations run as.
type botAuthorizer struct {
	server *Server
}

func (b botAuthorizer) Actor(ctx context.Context, telegramID int64) (telegrambot.Actor, bool) {
	if telegramID <= 0 {
		return telegrambot.Actor{}, false
	}
	var username string
	err := b.server.db.QueryRowContext(ctx, `SELECT username FROM admins WHERE telegram_id = ? AND role = ? AND status = ? LIMIT 1`,
		telegramID, string(adminapp.RoleFullAccess), string(adminapp.StatusActive)).Scan(&username)
	if err != nil {
		return telegrambot.Actor{}, false
	}
	admin, ok, err := b.server.adminRepo.AdminByUsername(ctx, username)
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
		ServiceID:       detail.ServiceID,
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

type botTrialService struct{ server *Server }

func (b botTrialService) store() shop.Trials {
	return shop.Trials{DB: b.server.db, Dialect: b.server.dialect}
}

func (b botTrialService) ClaimAndProvision(ctx context.Context, telegramID int64) (string, error) {
	store := b.store()
	claim, err := store.Claim(ctx, telegramID)
	if err != nil {
		return "", err
	}
	if claim.Status == "pending" {
		if err := (botShopService{server: b.server}).serviceHasActiveHost(ctx, claim.ServiceID); err != nil {
			return "", err
		}
	}
	return store.Provision(ctx, telegramID, botAccountProvisioner{server: b.server})
}

func (b botTrialService) Configure(ctx context.Context, config shop.TrialConfig) error {
	if err := (botShopService{server: b.server}).serviceHasActiveHost(ctx, config.ServiceID); err != nil {
		return err
	}
	return b.store().Configure(ctx, config)
}

func (b botTrialService) Disable(ctx context.Context) error { return b.store().Disable(ctx) }

func (b botShopService) stores() (shop.Orders, shop.Wallet) {
	return shop.Orders{DB: b.server.db}, shop.Wallet{DB: b.server.db, Dialect: b.server.dialect}
}

func (b botShopService) SalesOverview(ctx context.Context) (shop.SalesOverview, error) {
	orders, _ := b.stores()
	return orders.Overview(ctx)
}

func (b botShopService) Balance(ctx context.Context, telegramID int64) (int64, error) {
	_, wallet := b.stores()
	return wallet.Balance(ctx, telegramID)
}

func (b botShopService) History(ctx context.Context, telegramID int64) ([]shop.WalletEntry, error) {
	_, wallet := b.stores()
	return wallet.History(ctx, telegramID, 15)
}

func (b botShopService) Products(ctx context.Context) ([]shop.Product, error) {
	orders, _ := b.stores()
	return orders.ListProducts(ctx)
}

func (b botShopService) Orders(ctx context.Context, telegramID int64) ([]shop.Order, error) {
	orders, _ := b.stores()
	items, err := orders.ListCustomerOrders(ctx, telegramID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return items, nil
	}
	args := make([]any, len(items))
	for i := range items {
		args[i] = items[i].ID
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(items)), ",")
	rows, err := b.server.db.QueryContext(ctx, `SELECT order_id,kind FROM shop_order_changes WHERE order_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	actions := map[string]string{}
	for rows.Next() {
		var id, kind string
		if err := rows.Scan(&id, &kind); err != nil {
			return nil, err
		}
		actions[id] = kind
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Action = actions[items[i].ID]
	}
	return items, nil
}

func (b botShopService) Place(ctx context.Context, telegramID, productID int64, requestKey string) (shop.Order, error) {
	orders, _ := b.stores()
	return orders.Place(ctx, telegramID, productID, requestKey)
}

func (b botShopService) PlaceChange(ctx context.Context, telegramID, productID int64, username, kind, requestKey string) (shop.Order, error) {
	orders, _ := b.stores()
	return orders.PlaceChange(ctx, telegramID, productID, username, kind, requestKey)
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
	return b.deliverOrder(ctx, orderID)
}

func (b botShopService) deliverOrder(ctx context.Context, orderID string) (string, error) {
	orders, _ := b.stores()
	change, err := orders.Change(ctx, orderID)
	if err != nil {
		return "", err
	}
	if change == nil {
		return orders.Provision(ctx, orderID, botAccountProvisioner{server: b.server})
	}
	admin, ok, err := b.server.adminRepo.FirstFullAccessAdmin(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no active full-access admin for order delivery")
	}
	return orders.FulfillChange(ctx, orderID, func(ctx context.Context, tx *sql.Tx, order shop.Order, change shop.OrderChange) error {
		return b.server.userService.ApplyShopExtensionTx(ctx, tx, admin, order.TelegramID, order.ServiceID, change.Username, change.Kind, order.DataLimitBytes, order.DurationDays)
	})
}

type botAccountProvisioner struct{ server *Server }

type botTopupService struct{ server *Server }

func (b botTopupService) GatewayEnabled() bool { return b.server.gatewayEnabled() }
func (b botTopupService) CreateGateway(ctx context.Context, owner, amount int64, key string) (shop.GatewayPayment, error) {
	return b.server.createGatewayPayment(ctx, owner, amount, key)
}
func (b botTopupService) CheckGateway(ctx context.Context, owner int64, id string) (shop.GatewayPayment, error) {
	return b.server.gatewayPayments().Check(ctx, owner, id)
}
func (b botTopupService) ListGateway(ctx context.Context, owner int64) ([]shop.GatewayPayment, error) {
	return b.server.gatewayPayments().List(ctx, owner)
}

func (b botTopupService) store() shop.Topups {
	return shop.Topups{DB: b.server.db, Dialect: b.server.dialect}
}
func (b botTopupService) Get(ctx context.Context, id string) (shop.Topup, error) {
	return b.store().Get(ctx, id)
}
func (b botTopupService) Instructions(ctx context.Context) (string, error) {
	return b.store().Instructions(ctx)
}
func (b botTopupService) SetInstructions(ctx context.Context, instructions string) error {
	return b.store().SetInstructions(ctx, instructions)
}
func (b botTopupService) Create(ctx context.Context, telegramID, amount int64, requestKey string) (shop.Topup, error) {
	return b.store().Create(ctx, telegramID, amount, requestKey)
}
func (b botTopupService) SubmitReceipt(ctx context.Context, telegramID int64, topupID, fileID string) (shop.Topup, error) {
	return b.store().SubmitReceipt(ctx, telegramID, topupID, fileID)
}
func (b botTopupService) List(ctx context.Context, telegramID int64, adminPending bool) ([]shop.Topup, error) {
	return b.store().List(ctx, telegramID, adminPending)
}
func (b botTopupService) Approve(ctx context.Context, topupID string, adminID int64) (shop.Topup, error) {
	return b.store().Approve(ctx, topupID, adminID, shop.Wallet{DB: b.server.db, Dialect: b.server.dialect})
}
func (b botTopupService) Reject(ctx context.Context, topupID string, adminID int64) (shop.Topup, error) {
	return b.store().Reject(ctx, topupID, adminID)
}

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

type botNodesService struct{ server *Server }

func (b botNodesService) List(ctx context.Context) ([]telegrambot.NodeView, error) {
	result, err := b.server.nodeController.List(ctx, nodecontroller.Request{})
	if err != nil {
		return nil, err
	}
	views := make([]telegrambot.NodeView, 0, len(result.Nodes))
	for _, item := range result.Nodes {
		views = append(views, botNodeView(item))
	}
	return views, nil
}

func (b botNodesService) Get(ctx context.Context, id int64) (telegrambot.NodeView, error) {
	item, err := b.server.nodeController.Get(ctx, nodecontroller.Request{NodeID: id})
	if err != nil {
		return telegrambot.NodeView{}, err
	}
	return botNodeView(item), nil
}

func botNodeView(item nodecontroller.NodeListItem) telegrambot.NodeView {
	view := telegrambot.NodeView{
		ID: item.ID, Name: item.Name, Status: item.Status,
		AgentStatus: item.AgentStatus, XrayStatus: item.XrayStatus,
		CPUPercent: item.CPU.UsagePercent, MemoryPercent: item.Memory.UsagePercent,
		AppliedRevision: item.AppliedRevision, DesiredRevision: item.DesiredRevision,
	}
	if item.NodeServiceVersion != nil {
		view.AgentVersion = *item.NodeServiceVersion
	}
	if item.XrayVersion != nil {
		view.XrayVersion = *item.XrayVersion
	}
	return view
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
		Nodes:      botNodesService{server: s},
		Shop:       botShopService{server: s},
		Trials:     botTrialService{server: s},
		Support:    support.Store{DB: s.db},
		Topups:     botTopupService{server: s},
		System:     botSystemService{server: s},
		DB:         s.db,
	})
	b.Run(ctx)
}
