package server

import (
	"net/http"

	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/httpserver/oauth"
	"email-invoice-guard/internal/providers/microsoft"
	"email-invoice-guard/internal/providers/qq"
)

// New is the frozen cross-module constructor. It mounts the OAuth callback and
// QQ onboarding handlers when their sources are configured.
func New(cfg core.Config, store core.Store, ms *microsoft.Source, onboarding *qq.Onboarding) *http.Server {
	mounts := Mounts{}
	if ms != nil {
		mounts.OAuth = oauth.New(ms)
	}
	if onboarding != nil {
		mounts.QQ = onboarding
	}
	return NewWithMounts(cfg, store, mounts)
}
