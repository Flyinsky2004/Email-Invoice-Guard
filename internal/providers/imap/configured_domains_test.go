package imap

import (
	"email-invoice-guard/internal/core"
	"testing"
)

func TestConfiguredQQHostedAliasDomains(t *testing.T) {
	s := New(nil, core.IMAPConfig{AllowedDomains: []string{"qq.com", "foxmail.com", "mail.example.test"}})
	got, err := s.NormalizeAddress("user@mail.example.test")
	if err != nil || got != "user@mail.example.test" {
		t.Fatalf("explicit confirmed alias rejected: %q %v", got, err)
	}
	if _, err := s.NormalizeAddress("user@not-approved.test"); err == nil {
		t.Fatal("unlisted domain accepted")
	}
	defaults := New(nil, core.IMAPConfig{})
	if _, err := defaults.NormalizeAddress("user@mail.example.test"); err == nil {
		t.Fatal("custom domains must be opt-in")
	}
}
