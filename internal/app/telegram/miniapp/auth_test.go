package miniapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func signed(token string, at time.Time, user string) string {
	date := strconv.FormatInt(at.Unix(), 10)
	data := "auth_date=" + date + "\nuser=" + user
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(data))
	return "user=" + url.QueryEscape(user) + "&auth_date=" + date + "&hash=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyInitData(t *testing.T) {
	now := time.Unix(1800000000, 0)
	valid := signed("test-token", now, `{"id":321}`)
	if id, err := Verify(valid, "test-token", now); err != nil || id != 321 {
		t.Fatalf("valid initData: %d, %v", id, err)
	}
	for _, raw := range []string{
		valid + "&signature=unsigned",
		valid + "&user=%7B%22id%22%3A999%7D",
		signed("test-token", now, `{"id":999}`) + "x",
		signed("other-token", now, `{"id":321}`),
		signed("test-token", now.Add(-25*time.Hour), `{"id":321}`),
		signed("test-token", now.Add(2*time.Minute), `{"id":321}`),
	} {
		if _, err := Verify(raw, "test-token", now); err == nil {
			t.Fatalf("accepted invalid initData")
		}
	}
}
