package shop

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

type gatewayTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func TestZarinpalSandboxRequest(t *testing.T) {
	if os.Getenv("ANTIMAGE_TEST_ZARINPAL_SANDBOX") != "1" {
		t.Skip("opt-in external sandbox smoke test")
	}
	g := NewZarinpal("11111111-2222-3333-4444-555555555555", true, nil)
	if authority := os.Getenv("ANTIMAGE_TEST_ZARINPAL_AUTHORITY"); authority != "" {
		ref, err := g.Verify(t.Context(), 10000, authority)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("Sandbox payment verified; reference=%s", ref)
		return
	}
	authority, err := g.Request(t.Context(), 10000, "https://example.com/antimage-sandbox-return", uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Sandbox checkout: %s", g.CheckoutURL(authority))
}

func (t gatewayTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.target.Scheme
	u.Host = t.target.Host
	copy.URL = &u
	return t.base.RoundTrip(copy)
}

func TestZarinpalUsesStoredRialAmountAndVerifiesAlreadyPaid(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if input["amount"] != float64(12340) || input["merchant_id"] != "11111111-2222-3333-4444-555555555555" {
			t.Errorf("incorrect amount/merchant: %+v", input)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "request") {
			if input["currency"] != "IRR" {
				t.Error("ambiguous currency")
			}
			w.Write([]byte(`{"data":{"code":100,"authority":"S000000000000000000000000000000001"},"errors":[]}`))
		} else {
			w.Write([]byte(`{"data":{"code":101,"ref_id":201},"errors":[]}`))
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	g := NewZarinpal("11111111-2222-3333-4444-555555555555", true, &http.Client{Transport: gatewayTransport{u, http.DefaultTransport}})
	authority, err := g.Request(context.Background(), 12340, "https://example.com/return", "order-1")
	if err != nil {
		t.Fatal(err)
	}
	if ref, err := g.Verify(context.Background(), 12340, authority); err != nil || ref != "201" {
		t.Fatalf("verify=%s %v", ref, err)
	}
	if got := g.CheckoutURL(authority); got != "https://sandbox.zarinpal.com/pg/StartPay/"+authority {
		t.Fatal(got)
	}
	if len(paths) != 2 {
		t.Fatalf("requests=%v", paths)
	}
}

func TestZarinpalRejectsFailureAndRedirect(t *testing.T) {
	for _, mode := range []string{"failure", "redirect", "invalid-authority"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, "https://example.com/leak", 307)
					return
				}
				if mode == "invalid-authority" {
					w.Write([]byte(`{"data":{"code":100,"authority":"https://bad.example/"}}`))
					return
				}
				w.Write([]byte(`{"data":{"code":-51,"ref_id":201},"errors":[]}`))
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL)
			g := NewZarinpal("11111111-2222-3333-4444-555555555555", false, &http.Client{Transport: gatewayTransport{u, http.DefaultTransport}})
			if _, err := g.Request(context.Background(), 10000, "https://example.com/return", "order"); err == nil {
				t.Fatal("bad response accepted")
			}
		})
	}
}
