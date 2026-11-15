package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
	ErrUnauthorized    = errors.New("unauthorized")
	ErrInvalid         = errors.New("invalid input")
	ErrNoMatch         = errors.New("no parser matched")
	ErrReviewRequired  = errors.New("invoice review required")
	ErrReauthorization = errors.New("reauthorization required")
)

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random unavailable")
	}
	return hex.EncodeToString(b[:])
}
func TokenHash(token string) string {
	b := sha256.Sum256([]byte(token))
	return hex.EncodeToString(b[:])
}
func ParseDate(s string) (Date, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), "/", "-")
	t, e := time.Parse("2006-01-02", s)
	if e != nil {
		return "", fmt.Errorf("%w: date", ErrInvalid)
	}
	return Date(t.Format("2006-01-02")), nil
}
func LocalDate(t time.Time, loc *time.Location) Date { return Date(t.In(loc).Format("2006-01-02")) }
func DaysUntil(due, today Date) (int, error) {
	a, e := time.Parse("2006-01-02", string(due))
	if e != nil {
		return 0, e
	}
	b, e := time.Parse("2006-01-02", string(today))
	if e != nil {
		return 0, e
	}
	return int(a.Sub(b) / (24 * time.Hour)), nil
}
func CurrencyDecimals(c string) (int, bool) {
	switch c {
	case "JPY":
		return 0, true
	case "USD", "EUR", "CNY", "HKD", "TWD", "GBP", "AUD", "CAD", "SGD":
		return 2, true
	}
	return 0, false
}
func ParseMoney(s, currency string) (Money, error) {
	decimals, ok := CurrencyDecimals(currency)
	if !ok {
		return Money{}, fmt.Errorf("%w: currency", ErrInvalid)
	}
	s = strings.TrimSpace(s)
	sign := int64(1)
	if strings.HasPrefix(s, "-") {
		sign = -1
		s = s[1:]
	}
	p := strings.Split(s, ".")
	if len(p) > 2 || p[0] == "" {
		return Money{}, ErrInvalid
	}
	for _, v := range p {
		for _, r := range v {
			if r < '0' || r > '9' {
				return Money{}, ErrInvalid
			}
		}
	}
	frac := ""
	if len(p) == 2 {
		frac = p[1]
		if frac == "" {
			return Money{}, ErrInvalid
		}
	}
	if len(frac) > decimals {
		return Money{}, ErrInvalid
	}
	frac += strings.Repeat("0", decimals-len(frac))
	digits := p[0] + frac
	n, e := strconv.ParseInt(digits, 10, 64)
	if e != nil {
		return Money{}, ErrInvalid
	}
	return Money{Minor: sign * n, Currency: currency}, nil
}
func FormatMoney(m Money) string {
	n := m.Minor
	sign := ""
	s := strconv.FormatInt(n, 10)
	if strings.HasPrefix(s, "-") {
		sign = "-"
		s = s[1:]
	}
	d, ok := CurrencyDecimals(m.Currency)
	if !ok {
		return s + " " + m.Currency
	}
	if d == 0 {
		return sign + s + " " + m.Currency
	}
	for len(s) <= d {
		s = "0" + s
	}
	return sign + s[:len(s)-d] + "." + s[len(s)-d:] + " " + m.Currency
}
func TrustedInvoiceURL(raw, domain string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), domain) && u.User == nil && (u.Port() == "" || u.Port() == "443") && u.Fragment == ""
}
func Authorized(id int64, allowed []int64) bool {
	for _, v := range allowed {
		if id == v {
			return true
		}
	}
	return false
}
