package api

import (
	"embed"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/antimage/antimage/internal/app/shop"
	"github.com/antimage/antimage/internal/app/support"
	"github.com/antimage/antimage/internal/app/telegram/miniapp"
	"github.com/google/uuid"
)

//go:embed telegram_miniapp.html
var telegramMiniAppFiles embed.FS

func (s *Server) handleTelegramMiniAppPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	page, err := telegramMiniAppFiles.ReadFile("telegram_miniapp.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Mini App unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'unsafe-inline' https://telegram.org; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; frame-ancestors https://web.telegram.org https://*.telegram.org")
	_, _ = w.Write(page)
}

func (s *Server) handleTelegramMiniAppAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	settings, err := s.telegramRepo.Settings(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Telegram is unavailable")
		return
	}
	if !settings.UseTelegram || settings.APIToken == nil || strings.TrimSpace(*settings.APIToken) == "" {
		writeError(w, http.StatusServiceUnavailable, "Telegram is disabled")
		return
	}
	telegramID, err := miniapp.Verify(r.Header.Get("X-Telegram-Init-Data"), *settings.APIToken, time.Now())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Invalid Telegram authorization")
		return
	}
	shopService := botShopService{server: s}
	if strings.HasPrefix(r.URL.Path, "/api/telegram-app/tickets/") && strings.HasSuffix(r.URL.Path, "/replies") {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/telegram-app/tickets/"), "/replies")
		if uuid.Validate(id) != nil {
			writeError(w, http.StatusBadRequest, "Invalid ticket")
			return
		}
		var input struct {
			Message    string `json:"message"`
			RequestKey string `json:"request_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || uuid.Validate(input.RequestKey) != nil || len(input.Message) > 12000 || strings.TrimSpace(input.Message) == "" {
			writeError(w, http.StatusBadRequest, "Invalid support reply")
			return
		}
		ticket, err := (support.Store{DB: s.db}).ReplyCustomer(r.Context(), id, telegramID, input.Message, "tg-mini-reply:"+input.RequestKey)
		if errors.Is(err, support.ErrTicketNotFound) {
			writeError(w, http.StatusNotFound, "Ticket unavailable")
			return
		}
		if errors.Is(err, support.ErrTicketClosed) || errors.Is(err, support.ErrMessageConflict) {
			writeError(w, http.StatusConflict, "Support reply unavailable")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "Could not reply to ticket")
			return
		}
		writeJSON(w, http.StatusOK, ticket)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/telegram-app/tickets/") {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/telegram-app/tickets/")
		if uuid.Validate(id) != nil {
			writeError(w, http.StatusBadRequest, "Invalid ticket")
			return
		}
		ticket, err := (support.Store{DB: s.db}).Get(r.Context(), id, telegramID, false)
		if errors.Is(err, support.ErrTicketNotFound) {
			writeError(w, http.StatusNotFound, "Ticket unavailable")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not load ticket")
			return
		}
		writeJSON(w, http.StatusOK, ticket)
		return
	}
	switch r.URL.Path {
	case "/api/telegram-app/state":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		balance, err := shopService.Balance(r.Context(), telegramID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not load wallet")
			return
		}
		transactions, err := shopService.History(r.Context(), telegramID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not load wallet history")
			return
		}
		products, err := shopService.Products(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not load plans")
			return
		}
		orders, err := shopService.Orders(r.Context(), telegramID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not load orders")
			return
		}
		accounts, err := (botUserService{server: s}).ListByTelegramID(r.Context(), telegramID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not load services")
			return
		}
		topups, err := (botTopupService{server: s}).List(r.Context(), telegramID, false)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not load top-ups")
			return
		}
		tickets, err := (support.Store{DB: s.db}).List(r.Context(), telegramID, false)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not load support tickets")
			return
		}
		instructions, err := (botTopupService{server: s}).Instructions(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not load payment instructions")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"telegram_id": telegramID, "balance_toman": balance, "transactions": transactions, "products": products, "orders": orders, "services": accounts, "topups": topups, "tickets": tickets, "payment_instructions": instructions})
	case "/api/telegram-app/orders":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var input struct {
			ProductID  int64  `json:"product_id"`
			RequestKey string `json:"request_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.ProductID <= 0 || uuid.Validate(input.RequestKey) != nil {
			writeError(w, http.StatusBadRequest, "Invalid order request")
			return
		}
		order, err := shopService.Place(r.Context(), telegramID, input.ProductID, "tg-mini:"+input.RequestKey)
		if errors.Is(err, shop.ErrProductUnavailable) {
			writeError(w, http.StatusConflict, "Plan unavailable")
			return
		}
		if errors.Is(err, shop.ErrReferenceConflict) {
			writeError(w, http.StatusConflict, "Order request belongs to another customer or plan")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not create order")
			return
		}
		writeJSON(w, http.StatusOK, order)
	case "/api/telegram-app/pay":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var input struct {
			OrderID string `json:"order_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || uuid.Validate(input.OrderID) != nil {
			writeError(w, http.StatusBadRequest, "Invalid order")
			return
		}
		username, err := shopService.PayAndProvision(r.Context(), telegramID, input.OrderID)
		if errors.Is(err, shop.ErrInsufficientFunds) {
			writeError(w, http.StatusConflict, "Insufficient wallet balance")
			return
		}
		if errors.Is(err, shop.ErrOrderUnavailable) {
			writeError(w, http.StatusNotFound, "Order unavailable")
			return
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "Delivery pending; retry this order")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"username": username})
	case "/api/telegram-app/topups":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var input struct {
			AmountToman int64  `json:"amount_toman"`
			RequestKey  string `json:"request_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.AmountToman < 1000 || input.AmountToman > 100000000 || uuid.Validate(input.RequestKey) != nil {
			writeError(w, http.StatusBadRequest, "Invalid top-up request")
			return
		}
		instructions, err := (botTopupService{server: s}).Instructions(r.Context())
		if err != nil || strings.TrimSpace(instructions) == "" {
			writeError(w, http.StatusConflict, "Payment instructions are not configured")
			return
		}
		topup, err := (botTopupService{server: s}).Create(r.Context(), telegramID, input.AmountToman, "tg-mini-topup:"+input.RequestKey)
		if errors.Is(err, shop.ErrReferenceConflict) {
			writeError(w, http.StatusConflict, "Top-up request conflict")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not create top-up")
			return
		}
		writeJSON(w, http.StatusOK, topup)
	case "/api/telegram-app/tickets":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var input struct {
			Message    string `json:"message"`
			RequestKey string `json:"request_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || uuid.Validate(input.RequestKey) != nil || len(input.Message) > 12000 || strings.TrimSpace(input.Message) == "" {
			writeError(w, http.StatusBadRequest, "Invalid support request")
			return
		}
		ticket, err := (support.Store{DB: s.db}).Create(r.Context(), telegramID, input.Message, "tg-mini-ticket:"+input.RequestKey)
		if errors.Is(err, support.ErrMessageConflict) {
			writeError(w, http.StatusConflict, "Support request conflict")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "Could not create support ticket")
			return
		}
		writeJSON(w, http.StatusOK, ticket)
	default:
		writeError(w, http.StatusNotFound, "Not found")
	}
}
