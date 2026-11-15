package telegram

import (
	"context"
	"strconv"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

// menuInputKind identifies a one-shot text prompt opened from the menu.
type menuInputKind string

const (
	inputSearch       menuInputKind = "search"
	inputTimezone     menuInputKind = "tz"
	inputReminderTime menuInputKind = "rt"
	inputSyncInterval menuInputKind = "si"
)

// menuInputState remembers which bubble asked for text so the answer can edit
// that same message instead of starting a new one.
type menuInputState struct {
	Kind      menuInputKind
	ChatID    int64
	UserID    int64
	MessageID int64
	ExpiresAt time.Time
}

func (b *Bot) setMenuInput(st *menuInputState) {
	b.inputMu.Lock()
	b.inputs[wizardKey(st.ChatID, st.UserID)] = st
	b.inputMu.Unlock()
}

func (b *Bot) clearMenuInput(chatID, userID int64) {
	b.inputMu.Lock()
	delete(b.inputs, wizardKey(chatID, userID))
	b.inputMu.Unlock()
}

func (b *Bot) menuInputState(chatID, userID int64) *menuInputState {
	b.inputMu.Lock()
	defer b.inputMu.Unlock()
	return b.inputs[wizardKey(chatID, userID)]
}

// refreshMenuInput extends a still-active prompt after a rejected answer, so a
// typo near the TTL boundary does not discard the prompt.
func (b *Bot) refreshMenuInput(st *menuInputState) {
	b.inputMu.Lock()
	if current := b.inputs[wizardKey(st.ChatID, st.UserID)]; current == st {
		current.ExpiresAt = b.wizardNow().Add(b.wizardTTL())
	}
	b.inputMu.Unlock()
}

// menuInput consumes one plain-text message for a pending menu prompt. It
// returns handled=false when no prompt is active for this chat and user.
func (b *Bot) menuInput(ctx context.Context, m *message) (reply, int64, int64, bool) {
	st := b.menuInputState(m.Chat.ID, m.From.ID)
	if st == nil {
		return reply{}, 0, 0, false
	}
	if b.wizardNow().After(st.ExpiresAt) {
		b.clearMenuInput(m.Chat.ID, m.From.ID)
		tr := b.tr(ctx)
		return reply{
			parts:    []string{tr.t("menu.inputExpired")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, "", "")}},
		}, m.Chat.ID, st.MessageID, true
	}
	rep, keep := b.applyMenuInput(ctx, m, st)
	if keep {
		b.refreshMenuInput(st)
	} else {
		b.clearMenuInput(m.Chat.ID, m.From.ID)
	}
	return rep, m.Chat.ID, st.MessageID, true
}

func (b *Bot) applyMenuInput(ctx context.Context, m *message, st *menuInputState) (reply, bool) {
	tr := b.tr(ctx)
	text := strings.TrimSpace(m.Text)
	switch st.Kind {
	case inputSearch:
		if text == "" {
			return b.menuPromptReply(tr, menuSearch, tr.t("menu.prompt.searchEmpty")), true
		}
		emails, err := b.store.ListEmails(ctx, core.EmailFilter{Search: text, Limit: listLimit})
		if err != nil {
			b.log.Warn("telegram: menu search failed", "error", err.Error())
			return reply{
				parts:    []string{tr.t("error.commandFailed")},
				keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuMails, "")}},
			}, false
		}
		return b.menuSearchResultsReply(ctx, text, emails), false
	case inputTimezone:
		if _, err := time.LoadLocation(text); err != nil {
			return b.menuPromptReply(tr, menuTimezone, tr.t("settings.timezoneInvalid")), true
		}
		if err := b.store.SetSetting(ctx, settingTimezone, text); err != nil {
			b.log.Warn("telegram: save timezone failed", "error", err.Error())
			return reply{
				parts:    []string{tr.t("error.commandFailed")},
				keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuSettings, "")}},
			}, false
		}
		rep, err := b.menuSettingsReply(ctx)
		if err != nil {
			b.log.Warn("telegram: render settings failed", "error", err.Error())
			return reply{parts: []string{tr.t("error.commandFailed")}}, false
		}
		return rep, false
	case inputReminderTime:
		if !validReminderTime(text) {
			return b.menuPromptReply(tr, menuReminderTime, tr.t("settings.reminderInvalid")), true
		}
		if err := b.store.SetSetting(ctx, settingReminderTime, text); err != nil {
			b.log.Warn("telegram: save reminder time failed", "error", err.Error())
			return reply{
				parts:    []string{tr.t("error.commandFailed")},
				keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuSettings, "")}},
			}, false
		}
		rep, err := b.menuSettingsReply(ctx)
		if err != nil {
			b.log.Warn("telegram: render settings failed", "error", err.Error())
			return reply{parts: []string{tr.t("error.commandFailed")}}, false
		}
		return rep, false
	case inputSyncInterval:
		seconds, err := parsePositiveSeconds(text)
		if err != nil {
			return b.menuPromptReply(tr, menuSyncInterval, tr.t("settings.syncInvalid")), true
		}
		if err := b.store.SetSetting(ctx, settingSyncInterval, strconv.Itoa(seconds)); err != nil {
			b.log.Warn("telegram: save sync interval failed", "error", err.Error())
			return reply{
				parts:    []string{tr.t("error.commandFailed")},
				keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuSettings, "")}},
			}, false
		}
		rep, err := b.menuSettingsReply(ctx)
		if err != nil {
			b.log.Warn("telegram: render settings failed", "error", err.Error())
			return reply{parts: []string{tr.t("error.commandFailed")}}, false
		}
		return rep, false
	default:
		return reply{parts: []string{tr.t("error.unknownAction")}}, false
	}
}

// menuPrompt opens a text prompt in the current bubble and records where the
// answer should be rendered.
func (b *Bot) menuPrompt(ctx context.Context, cq *callbackQuery, view string) (*reply, string, bool, error) {
	tr := b.tr(ctx)
	kind, ok := menuInputKindFor(view)
	if !ok {
		return nil, tr.t("error.unknownAction"), false, nil
	}
	b.clearMenuInput(cq.Message.Chat.ID, cq.From.ID)
	if b.manual != nil {
		b.dropWizard(cq.Message.Chat.ID, cq.From.ID)
	}
	b.setMenuInput(&menuInputState{
		Kind:      kind,
		ChatID:    cq.Message.Chat.ID,
		UserID:    cq.From.ID,
		MessageID: cq.Message.MessageID,
		ExpiresAt: b.wizardNow().Add(b.wizardTTL()),
	})
	rep := b.menuPromptReply(tr, view, "")
	return &rep, "", false, nil
}

func (b *Bot) menuPromptReply(tr translator, view, errMsg string) reply {
	text := tr.t(menuPromptKey(view))
	if errMsg != "" {
		text = escape(errMsg) + "\n" + text
	}
	text += "\n" + tr.t("menu.prompt.hint")
	back := menuSettings
	if view == menuSearch {
		back = menuMails
	}
	return reply{
		parts:    []string{text},
		keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, back, "")}},
	}
}

func menuInputKindFor(view string) (menuInputKind, bool) {
	switch view {
	case menuSearch:
		return inputSearch, true
	case menuTimezone:
		return inputTimezone, true
	case menuReminderTime:
		return inputReminderTime, true
	case menuSyncInterval:
		return inputSyncInterval, true
	default:
		return "", false
	}
}

func menuPromptKey(view string) string {
	switch view {
	case menuSearch:
		return "menu.prompt.search"
	case menuTimezone:
		return "menu.prompt.timezone"
	case menuReminderTime:
		return "menu.prompt.reminderTime"
	case menuSyncInterval:
		return "menu.prompt.syncInterval"
	default:
		return "menu.prompt.hint"
	}
}
