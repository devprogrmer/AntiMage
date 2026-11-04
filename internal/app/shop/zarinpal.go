package shop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// Amounts passed to the gateway are always integer IRR (toman multiplied by 10).
// Protocol: https://www.zarinpal.com/docs/paymentGateway/connectToGateway
type PaymentGateway interface {
	Request(context.Context, int64, string, string) (string, error)
	Verify(context.Context, int64, string) (string, error)
	CheckoutURL(string) string
}
type GatewayFactory func(merchant string, sandbox bool) PaymentGateway

type Zarinpal struct {
	merchant, base string
	client         *http.Client
}

var authorityPattern = regexp.MustCompile(`^[AS][a-zA-Z0-9]{10,127}$`)

func NewZarinpal(merchant string, sandbox bool, client *http.Client) *Zarinpal {
	copy := http.Client{Timeout: 10 * time.Second}
	if client != nil {
		copy = *client
	}
	copy.Timeout = 10 * time.Second
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base := "https://payment.zarinpal.com"
	if sandbox {
		base = "https://sandbox.zarinpal.com"
	}
	return &Zarinpal{merchant: merchant, base: base, client: &copy}
}

func (g *Zarinpal) CheckoutURL(authority string) string {
	if !authorityPattern.MatchString(authority) {
		return ""
	}
	return g.base + "/pg/StartPay/" + authority
}
func (g *Zarinpal) Request(ctx context.Context, rial int64, callback, id string) (string, error) {
	u, err := url.Parse(callback)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || rial <= 0 {
		return "", fmt.Errorf("invalid gateway request")
	}
	var response struct {
		Data struct {
			Code      int    `json:"code"`
			Authority string `json:"authority"`
		} `json:"data"`
	}
	err = g.post(ctx, "request", map[string]any{"merchant_id": g.merchant, "amount": rial, "currency": "IRR", "callback_url": callback, "description": "AntiMage wallet payment", "metadata": map[string]string{"order_id": id}}, &response)
	if err != nil {
		return "", err
	}
	if response.Data.Code != 100 || !authorityPattern.MatchString(response.Data.Authority) {
		return "", fmt.Errorf("gateway request rejected (%d)", response.Data.Code)
	}
	return response.Data.Authority, nil
}
func (g *Zarinpal) Verify(ctx context.Context, rial int64, authority string) (string, error) {
	if rial <= 0 || !authorityPattern.MatchString(authority) {
		return "", fmt.Errorf("invalid gateway verification")
	}
	var response struct {
		Data struct {
			Code      int   `json:"code"`
			Reference int64 `json:"ref_id"`
		} `json:"data"`
	}
	err := g.post(ctx, "verify", map[string]any{"merchant_id": g.merchant, "amount": rial, "authority": authority}, &response)
	if err != nil {
		return "", err
	}
	if (response.Data.Code != 100 && response.Data.Code != 101) || response.Data.Reference <= 0 {
		return "", fmt.Errorf("payment not verified (%d)", response.Data.Code)
	}
	return strconv.FormatInt(response.Data.Reference, 10), nil
}
func (g *Zarinpal) post(ctx context.Context, method string, input, output any) error {
	if uuid.Validate(g.merchant) != nil {
		return fmt.Errorf("invalid gateway merchant ID")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.base+"/pg/v4/payment/"+method+".json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("gateway unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway HTTP status %d", res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(output); err != nil {
		return fmt.Errorf("invalid gateway response")
	}
	return nil
}
