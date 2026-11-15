package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

const (
	envelopeVersion = byte(1)
	gcmNonceSize    = 12
	gcmTagSize      = 16
	// timeLayout is fixed width so lexicographic TEXT ordering matches
	// chronological ordering (RFC3339Nano drops trailing zeros and breaks it).
	timeLayout = "2006-01-02T15:04:05.000000000Z"
)

func fmtTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt64Ptr(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

func nullDatePtr(d *core.Date) any {
	if d == nil {
		return nil
	}
	return string(*d)
}

func nullMoneyPtr(m *core.Money) any {
	if m == nil {
		return nil
	}
	return m.Minor
}

func moneyPtr(n sql.NullInt64, currency string) *core.Money {
	if !n.Valid {
		return nil
	}
	return &core.Money{Minor: n.Int64, Currency: currency}
}

func datePtr(n sql.NullString) *core.Date {
	if !n.Valid || n.String == "" {
		return nil
	}
	d := core.Date(n.String)
	return &d
}

func timePtr(n sql.NullString) *time.Time {
	if !n.Valid || n.String == "" {
		return nil
	}
	t := parseTime(n.String)
	return &t
}

func int64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal json: %w", err)
	}
	return string(b), nil
}

// seal encrypts plaintext with AES-256-GCM using a random nonce and a
// versioned envelope: [version][nonce][ciphertext+tag]. The additional data
// binds the ciphertext to its domain, account or session identity so blobs
// cannot be swapped between rows.
func (r *repo) seal(plaintext []byte, aad string) ([]byte, error) {
	block, err := aes.NewCipher(r.key)
	if err != nil {
		return nil, fmt.Errorf("cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	out := make([]byte, 1+len(nonce))
	out[0] = envelopeVersion
	copy(out[1:], nonce)
	out = gcm.Seal(out, nonce, plaintext, []byte(aad))
	return out, nil
}

func (r *repo) openSecret(blob []byte, aad string) ([]byte, error) {
	if len(blob) < 1+gcmNonceSize+gcmTagSize {
		return nil, fmt.Errorf("%w: encrypted blob too short", core.ErrInvalid)
	}
	if blob[0] != envelopeVersion {
		return nil, fmt.Errorf("%w: unsupported envelope version", core.ErrInvalid)
	}
	block, err := aes.NewCipher(r.key)
	if err != nil {
		return nil, fmt.Errorf("cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	nonce := blob[1 : 1+gcmNonceSize]
	ciphertext := blob[1+gcmNonceSize:]
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("%w: authentication failed", core.ErrUnauthorized)
	}
	return plain, nil
}

func credentialAAD(accountID string) string {
	return "credential|" + accountID
}

func sessionAAD(purpose, tokenHash string) string {
	return "session|" + purpose + "|" + tokenHash
}

// sanitizeDiagnostic strips control characters, collapses whitespace and
// bounds the length of externally supplied diagnostic text.
func sanitizeDiagnostic(s string, max int) string {
	if max <= 0 {
		max = 512
	}
	var b strings.Builder
	count := 0
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			b.WriteByte(' ')
			count++
			continue
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		count++
		if count >= max {
			break
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// hashBody returns a stable digest of the message body used by the conservative
// cross-folder fingerprint.
func hashBody(text, html string) [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte(text))
	h.Write([]byte{0})
	h.Write([]byte(html))
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}
