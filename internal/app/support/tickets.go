package support

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var ErrTicketNotFound = errors.New("support ticket not found")
var ErrTicketClosed = errors.New("support ticket closed")
var ErrMessageConflict = errors.New("support message reference conflict")

type Ticket struct {
	ID         string
	TelegramID int64
	Status     string
	Messages   []Message
}

type Message struct {
	ID      string
	Author  string
	Content string
}

type Store struct{ DB *sql.DB }

func (s Store) Create(ctx context.Context, telegramID int64, content, reference string) (Ticket, error) {
	if s.DB == nil {
		return Ticket{}, fmt.Errorf("support store unavailable")
	}
	if err := validate(telegramID, content, reference); err != nil {
		return Ticket{}, err
	}
	if existing, message, ok, err := s.byReference(ctx, reference); err != nil {
		return Ticket{}, err
	} else if ok {
		if existing.TelegramID != telegramID || message.Author != "customer" || message.Content != content {
			return Ticket{}, ErrMessageConflict
		}
		return existing, nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Ticket{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format("2006-01-02 15:04:05.000000")
	ticketID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO telegram_support_tickets (id, telegram_id, status, created_at, updated_at) VALUES (?, ?, 'open', ?, ?)`, ticketID, telegramID, now, now); err != nil {
		return Ticket{}, err
	}
	messageID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO telegram_support_messages (id, ticket_id, author, content, request_key, created_at) VALUES (?, ?, 'customer', ?, ?, ?)`, messageID, ticketID, content, reference, now); err != nil {
		_ = tx.Rollback()
		if existing, message, ok, lookupErr := s.byReference(ctx, reference); lookupErr == nil && ok {
			if existing.TelegramID != telegramID || message.Author != "customer" || message.Content != content {
				return Ticket{}, ErrMessageConflict
			}
			return existing, nil
		}
		return Ticket{}, err
	}
	if err := tx.Commit(); err != nil {
		return Ticket{}, err
	}
	return Ticket{ID: ticketID, TelegramID: telegramID, Status: "open", Messages: []Message{{ID: messageID, Author: "customer", Content: content}}}, nil
}

func (s Store) Reply(ctx context.Context, ticketID, content, reference string) (Ticket, error) {
	if s.DB == nil || len(ticketID) != 36 || strings.TrimSpace(content) == "" || utf8.RuneCountInString(content) > 3000 || !validReference(reference) {
		return Ticket{}, fmt.Errorf("invalid support reply")
	}
	if existing, message, ok, err := s.byReference(ctx, reference); err != nil {
		return Ticket{}, err
	} else if ok {
		if existing.ID != ticketID || message.Content != content || message.Author != "admin" {
			return Ticket{}, ErrMessageConflict
		}
		return existing, nil
	}
	ticket, err := s.Get(ctx, ticketID, 0, true)
	if err != nil {
		return Ticket{}, err
	}
	if ticket.Status != "open" {
		return Ticket{}, ErrTicketClosed
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05.000000")
	_, err = s.DB.ExecContext(ctx, `INSERT INTO telegram_support_messages (id, ticket_id, author, content, request_key, created_at) VALUES (?, ?, 'admin', ?, ?, ?)`, uuid.NewString(), ticketID, content, reference, now)
	if err != nil {
		if existing, message, ok, lookupErr := s.byReference(ctx, reference); lookupErr == nil && ok && existing.ID == ticketID && message.Author == "admin" && message.Content == content {
			return existing, nil
		}
		return Ticket{}, err
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE telegram_support_tickets SET updated_at = ? WHERE id = ?`, now, ticketID)
	return s.Get(ctx, ticketID, 0, true)
}

func (s Store) Get(ctx context.Context, ticketID string, telegramID int64, admin bool) (Ticket, error) {
	if s.DB == nil || len(ticketID) != 36 {
		return Ticket{}, ErrTicketNotFound
	}
	var ticket Ticket
	err := s.DB.QueryRowContext(ctx, `SELECT id, telegram_id, status FROM telegram_support_tickets WHERE id = ?`, ticketID).Scan(&ticket.ID, &ticket.TelegramID, &ticket.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return Ticket{}, ErrTicketNotFound
	}
	if err != nil {
		return Ticket{}, err
	}
	if !admin && ticket.TelegramID != telegramID {
		return Ticket{}, ErrTicketNotFound
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, author, content FROM telegram_support_messages WHERE ticket_id = ? ORDER BY created_at, id`, ticketID)
	if err != nil {
		return Ticket{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var message Message
		if err := rows.Scan(&message.ID, &message.Author, &message.Content); err != nil {
			return Ticket{}, err
		}
		ticket.Messages = append(ticket.Messages, message)
	}
	return ticket, rows.Err()
}

func (s Store) List(ctx context.Context, telegramID int64, admin bool) ([]Ticket, error) {
	if s.DB == nil || (!admin && telegramID <= 0) {
		return nil, fmt.Errorf("invalid support ticket list")
	}
	query := `SELECT id, telegram_id, status FROM telegram_support_tickets WHERE telegram_id = ? ORDER BY updated_at DESC LIMIT 20`
	args := []any{telegramID}
	if admin {
		query = `SELECT id, telegram_id, status FROM telegram_support_tickets WHERE status = 'open' ORDER BY updated_at DESC LIMIT 20`
		args = nil
	}
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Ticket{}
	for rows.Next() {
		var ticket Ticket
		if err := rows.Scan(&ticket.ID, &ticket.TelegramID, &ticket.Status); err != nil {
			return nil, err
		}
		result = append(result, ticket)
	}
	return result, rows.Err()
}

func (s Store) byReference(ctx context.Context, reference string) (Ticket, Message, bool, error) {
	var ticketID string
	var message Message
	err := s.DB.QueryRowContext(ctx, `SELECT ticket_id, id, author, content FROM telegram_support_messages WHERE request_key = ?`, reference).Scan(&ticketID, &message.ID, &message.Author, &message.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return Ticket{}, Message{}, false, nil
	}
	if err != nil {
		return Ticket{}, Message{}, false, err
	}
	ticket, err := s.Get(ctx, ticketID, 0, true)
	return ticket, message, err == nil, err
}

func validate(telegramID int64, content, reference string) error {
	if telegramID <= 0 || strings.TrimSpace(content) == "" || utf8.RuneCountInString(content) > 3000 || !validReference(reference) {
		return fmt.Errorf("invalid support ticket")
	}
	return nil
}

func validReference(reference string) bool {
	return reference != "" && len(reference) <= 128 && strings.TrimSpace(reference) == reference
}
