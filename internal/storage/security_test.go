package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func TestFixedWidthTimeOrdering(t *testing.T) {
	whole := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	fraction := whole.Add(123 * time.Millisecond)
	encodedWhole := fmtTime(whole)
	encodedFraction := fmtTime(fraction)
	if encodedWhole >= encodedFraction {
		t.Fatalf("TEXT ordering wrong: %q >= %q", encodedWhole, encodedFraction)
	}
	if len(encodedWhole) != len(encodedFraction) {
		t.Fatalf("time encoding is not fixed width: %q vs %q", encodedWhole, encodedFraction)
	}
	if !parseTime(encodedWhole).Equal(whole) || !parseTime(encodedFraction).Equal(fraction) {
		t.Fatal("time round trip failed")
	}
	// A whole-second value must sort before any fractional value in the same
	// second, which variable-width RFC3339Nano does not guarantee.
	sameSecond := []time.Time{
		whole,
		whole.Add(1 * time.Nanosecond),
		whole.Add(123 * time.Millisecond),
		whole.Add(999999999 * time.Nanosecond),
	}
	for i := 1; i < len(sameSecond); i++ {
		if fmtTime(sameSecond[i-1]) >= fmtTime(sameSecond[i]) {
			t.Fatalf("same-second ordering broken between %v and %v", sameSecond[i-1], sameSecond[i])
		}
	}
}

func TestClaimDeliverySameSecondBoundary(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	whole := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	if _, err := s.Enqueue(ctx, core.Delivery{DedupKey: "boundary-1", AvailableAt: whole}); err != nil {
		t.Fatal(err)
	}
	// available_at is a whole second; now is later within the same second.
	claimed, err := s.ClaimDelivery(ctx, whole.Add(123*time.Millisecond), time.Second)
	if err != nil || claimed == nil {
		t.Fatalf("same-second availability not honored: %v %v", claimed, err)
	}
	// Lease ends at whole+1.123s; a claim at whole+1.000s is not expired.
	if next, err := s.ClaimDelivery(ctx, whole.Add(time.Second), time.Minute); err != nil || next != nil {
		t.Fatalf("live lease reclaimed early: %v %v", next, err)
	}
	// At whole+1.200s the lease has expired and the job is retried.
	retried, err := s.ClaimDelivery(ctx, whole.Add(1200*time.Millisecond), time.Minute)
	if err != nil || retried == nil {
		t.Fatalf("expired same-second lease not retried: %v %v", retried, err)
	}
	if retried.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", retried.Attempts)
	}
}

func TestSessionExpirySameSecondBoundary(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	whole := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	hash := core.TokenHash("boundary-token")

	if err := s.PutSession(ctx, core.SecureSession{TokenHash: hash, Purpose: "oauth", Payload: []byte(`{"a":1}`), ExpiresAt: whole}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, hash, "oauth", whole.Add(-time.Nanosecond)); err != nil {
		t.Fatalf("session rejected before expiry: %v", err)
	}
	if _, err := s.GetSession(ctx, hash, "oauth", whole); err == nil {
		t.Fatal("session accepted at its exact expiry instant")
	}

	fractional := whole.Add(500 * time.Millisecond)
	if err := s.PutSession(ctx, core.SecureSession{TokenHash: hash, Purpose: "oauth", Payload: []byte(`{"a":2}`), ExpiresAt: fractional}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, hash, "oauth", whole.Add(123*time.Millisecond)); err != nil {
		t.Fatalf("fractional expiry misordered: %v", err)
	}
	if _, err := s.GetSession(ctx, hash, "oauth", fractional); err == nil {
		t.Fatal("fractional expiry accepted at its exact instant")
	}
}

func TestSessionGetDoesNotConsumeAndReplayFails(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	hash := core.TokenHash("session-token")
	payload := []byte(`{"state":"abc","verifier":"xyz"}`)
	expiry := time.Now().Add(time.Hour)
	if err := s.PutSession(ctx, core.SecureSession{TokenHash: hash, Purpose: "oauth", Payload: payload, ExpiresAt: expiry}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, err := s.GetSession(ctx, hash, "oauth", time.Now())
		if err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		if string(got.Payload) != string(payload) || got.Purpose != "oauth" {
			t.Fatalf("session payload = %+v", got)
		}
	}
	consumed, err := s.ConsumeSession(ctx, hash, "oauth", time.Now())
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if string(consumed.Payload) != string(payload) {
		t.Fatalf("consumed payload = %+v", consumed)
	}
	if _, err := s.ConsumeSession(ctx, hash, "oauth", time.Now()); err == nil {
		t.Fatal("replay after consume succeeded")
	}
	if _, err := s.GetSession(ctx, hash, "oauth", time.Now()); err == nil {
		t.Fatal("consumed session still readable")
	}
}

func TestSessionPurposeMismatchRejected(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	hash := core.TokenHash("purpose-token")
	if err := s.PutSession(ctx, core.SecureSession{TokenHash: hash, Purpose: "oauth", Payload: []byte("p"), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, hash, "other", time.Now()); err == nil {
		t.Fatal("purpose mismatch accepted")
	}
	if _, err := s.ConsumeSession(ctx, hash, "other", time.Now()); err == nil {
		t.Fatal("purpose mismatch consumed")
	}
	if _, err := s.ConsumeSession(ctx, hash, "oauth", time.Now()); err != nil {
		t.Fatalf("valid purpose rejected after mismatch: %v", err)
	}
}

func TestSessionEncryptedAndTamperProof(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	hash := core.TokenHash("tamper-token")
	secret := "super-secret-payload"
	if err := s.PutSession(ctx, core.SecureSession{TokenHash: hash, Purpose: "oauth", Payload: []byte(secret), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var blob []byte
	if err := s.DB().QueryRowContext(ctx, `SELECT encrypted_payload FROM oauth_sessions WHERE token_hash = ?`, hash).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), secret) {
		t.Fatal("session payload stored in plaintext")
	}
	blob[len(blob)-1] ^= 0xff
	if _, err := s.DB().ExecContext(ctx, `UPDATE oauth_sessions SET encrypted_payload = ? WHERE token_hash = ?`, blob, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, hash, "oauth", time.Now()); err == nil {
		t.Fatal("tampered session accepted")
	}
}

func TestPutSessionValidation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.PutSession(ctx, core.SecureSession{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("empty session = %v", err)
	}
	if err := s.PutSession(ctx, core.SecureSession{TokenHash: "h", Purpose: "p"}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("session without expiry = %v", err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.GetSetting(ctx, "timezone"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing setting = %v", err)
	}
	if err := s.SetSetting(ctx, "timezone", "Asia/Taipei"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "timezone", "UTC"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSetting(ctx, "timezone")
	if err != nil || got != "UTC" {
		t.Fatalf("setting = %q %v", got, err)
	}
	if err := s.SetSetting(ctx, "", "x"); err == nil {
		t.Fatal("empty setting key accepted")
	}
}

func TestStatusCounts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-status", core.Microsoft, "status@example.com")
	mustEmail(t, s, sampleEmail("acc-status", "status-mail"))
	mustInvoice(t, s, sampleInvoice("acc-status", "INV-STATUS"))
	if _, err := s.Enqueue(ctx, core.Delivery{DedupKey: "status-1"}); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Accounts != 1 || st.Emails != 1 || st.PendingInvoices != 1 || st.PendingDeliveries != 1 {
		t.Fatalf("status = %+v", st)
	}
}
