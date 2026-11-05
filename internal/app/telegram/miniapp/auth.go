package miniapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidInitData = errors.New("invalid Telegram Mini App init data")

// Verify authenticates Telegram initData and returns its user ID. The raw
// initDataUnsafe object supplied by the browser must never authorize requests.
func Verify(raw, token string, now time.Time) (int64, error) {
	if len(raw) == 0 || len(raw) > 8192 || token == "" {
		return 0, ErrInvalidInitData
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return 0, ErrInvalidInitData
	}
	parts := strings.Split(raw, "&")
	if len(parts) != len(values) {
		return 0, ErrInvalidInitData
	}
	for _, list := range values {
		if len(list) != 1 {
			return 0, ErrInvalidInitData
		}
	}
	hashBytes, err := hex.DecodeString(values.Get("hash"))
	if err != nil || len(hashBytes) != sha256.Size {
		return 0, ErrInvalidInitData
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "hash" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+values.Get(key))
	}
	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	secretMAC.Write([]byte(token))
	dataMAC := hmac.New(sha256.New, secretMAC.Sum(nil))
	dataMAC.Write([]byte(strings.Join(lines, "\n")))
	if !hmac.Equal(hashBytes, dataMAC.Sum(nil)) {
		return 0, ErrInvalidInitData
	}
	seconds, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil || seconds <= 0 {
		return 0, ErrInvalidInitData
	}
	authTime := time.Unix(seconds, 0)
	if authTime.After(now.Add(time.Minute)) || now.Sub(authTime) > 24*time.Hour {
		return 0, ErrInvalidInitData
	}
	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID <= 0 {
		return 0, ErrInvalidInitData
	}
	return user.ID, nil
}
