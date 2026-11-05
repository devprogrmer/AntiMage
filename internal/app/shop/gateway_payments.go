package shop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"net/url"
	"time"
)

var ErrPaymentUnavailable = errors.New("gateway payment unavailable")
var ErrPaymentCreating = errors.New("gateway payment is being created; retry the same request")

type GatewayPayment struct {
	ID          string
	TelegramID  int64 `json:"-"`
	AmountToman int64
	Status      string
	CheckoutURL string
}
type paymentRecord struct {
	GatewayPayment
	merchant             string
	sandbox              bool
	authority, reference sql.NullString
}
type GatewayPayments struct {
	DB      *sql.DB
	Dialect string
	Factory GatewayFactory
}

func (s GatewayPayments) gateway(p paymentRecord) PaymentGateway {
	if s.Factory != nil {
		return s.Factory(p.merchant, p.sandbox)
	}
	return NewZarinpal(p.merchant, p.sandbox, nil)
}

const paymentColumns = `id,telegram_id,amount_toman,status,merchant_id,sandbox,authority,reference`

func scanGatewayPayment(row interface{ Scan(...any) error }) (paymentRecord, error) {
	var p paymentRecord
	err := row.Scan(&p.ID, &p.TelegramID, &p.AmountToman, &p.Status, &p.merchant, &p.sandbox, &p.authority, &p.reference)
	return p, err
}
func (s GatewayPayments) record(ctx context.Context, id string) (paymentRecord, error) {
	p, err := scanGatewayPayment(s.DB.QueryRowContext(ctx, `SELECT `+paymentColumns+` FROM shop_gateway_payments WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrPaymentUnavailable
	}
	return p, err
}
func (s GatewayPayments) view(p paymentRecord) GatewayPayment {
	if p.Status == "awaiting" && p.authority.Valid {
		p.CheckoutURL = s.gateway(p).CheckoutURL(p.authority.String)
	}
	return p.GatewayPayment
}
func (s GatewayPayments) Get(ctx context.Context, id string) (GatewayPayment, error) {
	p, err := s.record(ctx, id)
	if err != nil {
		return GatewayPayment{}, err
	}
	return s.view(p), nil
}

func (s GatewayPayments) Check(ctx context.Context, owner int64, id string) (GatewayPayment, error) {
	p, err := s.record(ctx, id)
	if err != nil {
		return GatewayPayment{}, err
	}
	if owner <= 0 || p.TelegramID != owner || !p.authority.Valid {
		return GatewayPayment{}, ErrPaymentUnavailable
	}
	return s.Verify(ctx, id, p.authority.String)
}
func (s GatewayPayments) List(ctx context.Context, owner int64) ([]GatewayPayment, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+paymentColumns+` FROM shop_gateway_payments WHERE telegram_id=? ORDER BY created_at DESC,id DESC LIMIT 20`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []GatewayPayment{}
	for rows.Next() {
		p, err := scanGatewayPayment(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, s.view(p))
	}
	return items, rows.Err()
}

func (s GatewayPayments) Create(ctx context.Context, owner, amount int64, key, merchant string, sandbox bool, callback string) (GatewayPayment, error) {
	u, err := url.Parse(callback)
	if s.DB == nil || owner <= 0 || amount < 1000 || amount > 100000000 || !validTopupReference(key) || uuid.Validate(merchant) != nil || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return GatewayPayment{}, ErrPaymentUnavailable
	}
	now := time.Now().UTC()
	stamp := now.Format("2006-01-02 15:04:05")
	id := uuid.NewString()
	_, insertErr := s.DB.ExecContext(ctx, `INSERT INTO shop_gateway_payments (id,request_key,telegram_id,amount_toman,merchant_id,sandbox,status,lease_until,created_at,updated_at) VALUES (?,?,?,?,?,?,'creating',0,?,?)`, id, key, owner, amount, merchant, sandbox, stamp, stamp)
	p, err := scanGatewayPayment(s.DB.QueryRowContext(ctx, `SELECT `+paymentColumns+` FROM shop_gateway_payments WHERE request_key=?`, key))
	if err != nil {
		if insertErr != nil {
			return GatewayPayment{}, insertErr
		}
		return GatewayPayment{}, err
	}
	if p.TelegramID != owner || p.AmountToman != amount {
		return GatewayPayment{}, ErrReferenceConflict
	}
	if p.Status != "creating" {
		return s.view(p), nil
	}
	lease := now.Unix() + 30
	result, err := s.DB.ExecContext(ctx, `UPDATE shop_gateway_payments SET lease_until=?,updated_at=? WHERE id=? AND status='creating' AND lease_until<=?`, lease, stamp, p.ID, now.Unix())
	if err != nil {
		return GatewayPayment{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return GatewayPayment{}, err
	}
	if count != 1 {
		return GatewayPayment{}, ErrPaymentCreating
	}
	query := u.Query()
	query.Set("payment", p.ID)
	u.RawQuery = query.Encode()
	authority, err := s.gateway(p).Request(ctx, p.AmountToman*10, u.String(), p.ID)
	if err != nil {
		_, _ = s.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE shop_gateway_payments SET lease_until=0 WHERE id=? AND status='creating' AND lease_until=?`, p.ID, lease)
		return GatewayPayment{}, err
	}
	result, err = s.DB.ExecContext(ctx, `UPDATE shop_gateway_payments SET authority=?,status='awaiting',lease_until=0,updated_at=? WHERE id=? AND status='creating' AND lease_until=?`, authority, stamp, p.ID, lease)
	if err != nil {
		return GatewayPayment{}, err
	}
	count, err = result.RowsAffected()
	if err != nil {
		return GatewayPayment{}, err
	}
	if count != 1 {
		return GatewayPayment{}, ErrPaymentCreating
	}
	return s.Get(ctx, p.ID)
}

func (s GatewayPayments) Verify(ctx context.Context, id, authority string) (GatewayPayment, error) {
	p, err := s.record(ctx, id)
	if err != nil {
		return GatewayPayment{}, err
	}
	if !p.authority.Valid || p.authority.String != authority || p.AmountToman <= 0 || p.AmountToman > 100000000 {
		return GatewayPayment{}, ErrPaymentUnavailable
	}
	if p.Status == "paid" {
		return s.view(p), nil
	}
	if p.Status == "awaiting" {
		ref, err := s.gateway(p).Verify(ctx, p.AmountToman*10, authority)
		if err != nil {
			return GatewayPayment{}, err
		}
		if ref == "" || len(ref) > 100 {
			return GatewayPayment{}, ErrPaymentUnavailable
		}
		// Provider reference is unique per environment. A second local payment
		// cannot claim the same bank transaction.
		reference := fmt.Sprintf("%t:%s", p.sandbox, ref)
		if _, err := s.DB.ExecContext(ctx, `UPDATE shop_gateway_payments SET status='verified',reference=?,updated_at=? WHERE id=? AND status='awaiting'`, reference, time.Now().UTC().Format("2006-01-02 15:04:05"), p.ID); err != nil {
			return GatewayPayment{}, err
		}
		p, err = s.record(ctx, id)
		if err != nil {
			return GatewayPayment{}, err
		}
	}
	if p.Status == "paid" {
		return s.view(p), nil
	}
	if p.Status != "verified" || !p.reference.Valid {
		return GatewayPayment{}, ErrPaymentUnavailable
	}
	return s.settle(ctx, p)
}

func (s GatewayPayments) settle(ctx context.Context, p paymentRecord) (GatewayPayment, error) {
	// The journal reference survives a crash after credit and before settlement.
	// Replaying it can never increase the wallet balance twice.
	if _, err := (Wallet{DB: s.DB, Dialect: s.Dialect}).Credit(ctx, p.TelegramID, p.AmountToman, "gateway:"+p.ID); err != nil {
		return GatewayPayment{}, err
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE shop_gateway_payments SET status='paid',updated_at=? WHERE id=? AND status='verified'`, time.Now().UTC().Format("2006-01-02 15:04:05"), p.ID); err != nil {
		return GatewayPayment{}, err
	}
	return s.Get(ctx, p.ID)
}

func (s GatewayPayments) RecoverVerified(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM shop_gateway_payments WHERE status='verified' ORDER BY created_at,id LIMIT 25`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		p, err := s.record(ctx, id)
		if err != nil {
			return err
		}
		if p.Status == "verified" && p.reference.Valid {
			if _, err := s.settle(ctx, p); err != nil {
				return err
			}
		}
	}
	return nil
}
