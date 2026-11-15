//go:build stageb

package server

import (
	"net/http"

	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/httpserver/oauth"
	"email-invoice-guard/internal/providers/microsoft"
	"email-invoice-guard/internal/providers/qq"
)

// New is the frozen cross-module constructor. It is compiled only once the
// provider, OAuth and QQ onboarding packages have been merged (Stage B), which
// is why it carries the "stageb" build tag during Stage A.
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
