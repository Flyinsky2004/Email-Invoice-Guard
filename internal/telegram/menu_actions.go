package telegram

import (
	"context"
	"fmt"
	"strings"

	"email-invoice-guard/internal/core"
)

// menuAddAction starts provider onboarding and shows the one-time link in the
// same bubble.
func (b *Bot) menuAddAction(ctx context.Context, cq *callbackQuery, provider string) (*reply, string, bool, error) {
	tr := b.tr(ctx)
	pt := core.ProviderType(strings.ToLower(provider))
	source, ok := b.onboarding[pt]
	if !ok || source == nil {
		return &reply{
			parts:    []string{tr.t("add.unsupported")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuAccounts, "")}},
		}, "", false, nil
	}
	url, err := source.Begin(ctx, cq.From.ID)
	if err != nil {
		return nil, "", false, fmt.Errorf("begin onboarding: %w", err)
	}
	if strings.TrimSpace(url) == "" {
		return &reply{
			parts:    []string{tr.t("add.noLink")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuAccounts, "")}},
		}, "", false, nil
	}
	rows := make([][]inlineButton, 0, 2)
	if trustedHTTPSURL(url) {
		rows = append(rows, []inlineButton{{Text: tr.t("menu.button.openLink"), URL: url}})
	}
	rows = append(rows, menuNavRow(tr, menuAccounts, ""))
	return &reply{
		parts:    []string{tr.t("add.openLink", string(pt), escape(url))},
		keyboard: &inlineKeyboard{InlineKeyboard: rows},
	}, "", false, nil
}

// menuRemoveDo performs the confirmed soft removal and re-renders the account
// list.
func (b *Bot) menuRemoveDo(ctx context.Context, cq *callbackQuery, accountID string) (*reply, string, bool, error) {
	tr := b.tr(ctx)
	account, err := b.store.GetAccount(ctx, accountID)
	if err != nil || account.OwnerUserID != cq.From.ID {
		return &reply{
			parts:    []string{tr.t("error.accountNotFound")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuAccounts, "")}},
		}, "", false, nil
	}
	if err := b.store.RemoveAccount(ctx, account.ID); err != nil {
		b.log.Warn("telegram: remove account failed", "error", err.Error())
		return nil, tr.t("error.removalFailed"), true, nil
	}
	rep, err := b.menuAccountsReply(ctx)
	if err != nil {
		return nil, "", false, err
	}
	return &rep, tr.t("remove.disabledShort"), false, nil
}

// menuSyncAction starts a background sync and keeps the result in the bubble.
func (b *Bot) menuSyncAction(ctx context.Context) (*reply, string, bool, error) {
	tr := b.tr(ctx)
	rep, err := b.commandSync(ctx)
	if err != nil {
		return nil, "", false, err
	}
	rep.keyboard = &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, "", "")}}
	return &rep, "", false, nil
}

// menuSetLanguage stores the selected language and re-renders settings in it.
func (b *Bot) menuSetLanguage(ctx context.Context, raw string) (*reply, string, bool, error) {
	lg, ok := parseLanguage(raw)
	if !ok {
		return nil, b.text(ctx, "settings.languageInvalid"), true, nil
	}
	if err := b.store.SetSetting(ctx, settingLanguage, string(lg)); err != nil {
		return nil, "", false, fmt.Errorf("save language: %w", err)
	}
	rep, err := b.menuSettingsReply(ctx)
	if err != nil {
		return nil, "", false, err
	}
	return &rep, translator{lang: lg}.t("settings.languageSet", string(lg)), false, nil
}

// menuStartWizard opens the manual-plan wizard inside the current bubble. The
// wizard's text steps continue as their own prompts; its buttons keep editing
// the same message.
func (b *Bot) menuStartWizard(ctx context.Context, cq *callbackQuery, planID string) (*reply, string, bool, error) {
	tr := b.tr(ctx)
	if b.manual == nil {
		return &reply{
			parts:    []string{tr.t("error.manualNotConfigured")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuReminders, "")}},
		}, "", false, nil
	}
	b.clearMenuInput(cq.Message.Chat.ID, cq.From.ID)
	rep, err := b.startWizard(ctx, cq.From.ID, cq.Message.Chat.ID, planID)
	if err != nil {
		return nil, "", false, err
	}
	return &rep, "", false, nil
}
