package telegram

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"email-invoice-guard/internal/core"
)

// wizardAssetOptionLimit caps the asset buttons so the keyboard stays well
// below Telegram's 100-button ceiling.
const wizardAssetOptionLimit = 10

// wizardStep is one field of the manual-plan creation wizard.
type wizardStep int

const (
	wzStepTitle wizardStep = iota
	wzStepPeriod
	wzStepProvider
	wzStepAmount
	wzStepCurrency
	wzStepBillingDay
	wzStepAsset
	wzStepReminderTime
	wzStepTimezone
	wzStepPaymentURL
	wzStepNotes
	wzStepConfirm
)

// wizardState is per-user, per-chat in-memory wizard state. It expires after
// cfg.Manual.WizardTTL and is never resumed silently.
type wizardState struct {
	ID             string
	ChatID, UserID int64
	Step           wizardStep
	PlanID         string

	Title, ProviderName, Currency string
	Period                        string
	AmountMinor                   int64
	HasAmount                     bool
	BillingDay                    int
	ReminderTime, Timezone        string
	PaymentURL, Notes             string
	// AssetID and Description are not free-text steps: AssetID is selected in
	// step 6 and Description is preserved when an existing plan is edited.
	AssetID, Description string

	// Asset options are short callback tokens mapped to asset IDs so every
	// callback_data stays within 64 bytes.
	AssetOptions map[string]string
	AssetLabels  map[string]string
	AssetTokens  []string
	AssetNote    string

	// pendingAmount holds an amount typed without a currency until the
	// currency selection step completes.
	pendingAmount string

	ExpiresAt time.Time
}

func wizardKey(chatID, userID int64) string {
	return fmt.Sprintf("%d:%d", chatID, userID)
}

func (b *Bot) wizardTTL() time.Duration {
	if d := b.cfg.Manual.WizardTTL; d > 0 {
		return d
	}
	return 10 * time.Minute
}

func (b *Bot) wizardNow() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *Bot) defaultWizardTime() string {
	if validReminderTime(b.cfg.Manual.DefaultTime) {
		return strings.TrimSpace(b.cfg.Manual.DefaultTime)
	}
	return "09:00"
}

func (b *Bot) defaultWizardTimezone() string {
	if tz := strings.TrimSpace(b.cfg.Manual.DefaultTimezone); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if tz := strings.TrimSpace(b.cfg.Timezone); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	return "Asia/Taipei"
}

// startWizard creates or replaces the active wizard for this user and chat.
// An edit wizard loads the plan first and opens on the confirmation step.
func (b *Bot) startWizard(ctx context.Context, userID, chatID int64, planID string) (reply, error) {
	tr := b.tr(ctx)
	st := &wizardState{
		ID:           core.NewID(),
		ChatID:       chatID,
		UserID:       userID,
		Step:         wzStepTitle,
		PlanID:       planID,
		Period:       "monthly",
		ReminderTime: b.defaultWizardTime(),
		Timezone:     b.defaultWizardTimezone(),
		ExpiresAt:    b.wizardNow().Add(b.wizardTTL()),
	}
	if planID != "" {
		plan, err := b.manual.GetPlan(ctx, planID)
		if err != nil {
			return b.manualReadReply(tr, "get manual plan", err)
		}
		st.prefill(plan)
		st.Step = wzStepConfirm
	}
	b.wizardMu.Lock()
	b.wizards[wizardKey(chatID, userID)] = st
	b.wizardMu.Unlock()
	return b.wizardPrompt(tr, st, ""), nil
}

func (st *wizardState) prefill(plan core.ManualPlan) {
	st.Title = plan.Title
	st.ProviderName = plan.ProviderName
	st.Period = "monthly"
	if plan.Amount != nil {
		st.HasAmount = true
		st.AmountMinor = plan.Amount.Minor
		st.Currency = plan.Amount.Currency
	}
	st.BillingDay = plan.BillingDay
	if plan.ReminderTime != "" {
		st.ReminderTime = plan.ReminderTime
	}
	if plan.Timezone != "" {
		st.Timezone = plan.Timezone
	}
	st.PaymentURL = plan.PaymentURL
	st.Notes = plan.Notes
	st.AssetID = plan.AssetID
	st.Description = plan.Description
}

// wizardInput consumes a plain-text message for an active wizard. It returns
// handled=false when no wizard is active for this user and chat.
func (b *Bot) wizardInput(ctx context.Context, m *message) (reply, bool) {
	key := wizardKey(m.Chat.ID, m.From.ID)
	b.wizardMu.Lock()
	st := b.wizards[key]
	if st == nil {
		b.wizardMu.Unlock()
		return reply{}, false
	}
	if b.wizardNow().After(st.ExpiresAt) {
		delete(b.wizards, key)
		b.wizardMu.Unlock()
		return reply{parts: []string{b.text(ctx, "wizard.expired")}}, true
	}
	rep := b.wizardApplyText(ctx, st, strings.TrimSpace(m.Text))
	st.ExpiresAt = b.wizardNow().Add(b.wizardTTL())
	b.wizardMu.Unlock()
	return rep, true
}

// wizardApplyText validates one text answer, stores it and returns the next
// prompt. A validation failure keeps the current step.
func (b *Bot) wizardApplyText(ctx context.Context, st *wizardState, text string) reply {
	tr := b.tr(ctx)
	switch st.Step {
	case wzStepTitle:
		if text == "" || utf8.RuneCountInString(text) > manualMaxTitle {
			return b.wizardPrompt(tr, st, tr.t("wizard.err.title"))
		}
		st.Title = text
		st.Step = wzStepPeriod
	case wzStepPeriod:
		if !strings.EqualFold(text, "monthly") {
			return b.wizardPrompt(tr, st, tr.t("wizard.err.period"))
		}
		st.Period = "monthly"
		st.Step = wzStepProvider
	case wzStepProvider:
		if text == "-" {
			text = ""
		}
		if utf8.RuneCountInString(text) > manualMaxProvider {
			return b.wizardPrompt(tr, st, tr.t("wizard.err.provider"))
		}
		st.ProviderName = text
		st.Step = wzStepAmount
	case wzStepAmount:
		if text == "-" {
			st.HasAmount = false
			st.AmountMinor = 0
			st.Currency = ""
			st.pendingAmount = ""
			st.Step = wzStepBillingDay
			break
		}
		fields := strings.Fields(text)
		switch len(fields) {
		case 1:
			if !validAmountDigits(fields[0]) {
				return b.wizardPrompt(tr, st, tr.t("wizard.err.amount"))
			}
			st.pendingAmount = fields[0]
			st.Step = wzStepCurrency
		case 2:
			currency := strings.ToUpper(fields[1])
			money, err := core.ParseMoney(fields[0], currency)
			if err != nil || money.Minor < 0 {
				return b.wizardPrompt(tr, st, tr.t("wizard.err.amountInvalid"))
			}
			st.HasAmount = true
			st.AmountMinor = money.Minor
			st.Currency = currency
			st.pendingAmount = ""
			st.Step = wzStepBillingDay
		default:
			return b.wizardPrompt(tr, st, tr.t("wizard.err.amount"))
		}
	case wzStepCurrency:
		currency := strings.ToUpper(strings.TrimSpace(text))
		if _, ok := core.CurrencyDecimals(currency); !ok {
			return b.wizardPrompt(tr, st, tr.t("wizard.err.currency"))
		}
		return b.applyWizardCurrency(tr, st, currency)
	case wzStepBillingDay:
		day, ok := parseBillingDay(text)
		if !ok {
			return b.wizardPrompt(tr, st, tr.t("wizard.err.billingDay"))
		}
		st.BillingDay = day
		st.Step = wzStepAsset
		b.prepareAssetStep(ctx, tr, st)
	case wzStepAsset:
		return b.wizardPrompt(tr, st, tr.t("wizard.err.asset"))
	case wzStepReminderTime:
		if text != "-" {
			if !validReminderTime(text) {
				return b.wizardPrompt(tr, st, tr.t("wizard.err.reminderTime"))
			}
			st.ReminderTime = text
		}
		st.Step = wzStepTimezone
	case wzStepTimezone:
		if text != "-" {
			if _, err := time.LoadLocation(text); err != nil {
				return b.wizardPrompt(tr, st, tr.t("wizard.err.timezone"))
			}
			st.Timezone = text
		}
		st.Step = wzStepPaymentURL
	case wzStepPaymentURL:
		if text == "-" {
			st.PaymentURL = ""
			st.Step = wzStepNotes
			break
		}
		if utf8.RuneCountInString(text) > manualMaxURL || !trustedHTTPSURL(text) {
			return b.wizardPrompt(tr, st, tr.t("wizard.err.paymentURL"))
		}
		st.PaymentURL = text
		st.Step = wzStepNotes
	case wzStepNotes:
		if text == "-" {
			text = ""
		}
		if utf8.RuneCountInString(text) > manualMaxNotes {
			return b.wizardPrompt(tr, st, tr.t("wizard.err.notes"))
		}
		st.Notes = text
		st.Step = wzStepConfirm
	case wzStepConfirm:
		return b.wizardPrompt(tr, st, tr.t("wizard.err.confirm"))
	}
	return b.wizardPrompt(tr, st, "")
}

// applyWizardCurrency completes the amount step after a currency selection.
func (b *Bot) applyWizardCurrency(tr translator, st *wizardState, currency string) reply {
	if st.pendingAmount == "" {
		st.Step = wzStepAmount
		return b.wizardPrompt(tr, st, tr.t("wizard.err.sendAmountFirst"))
	}
	money, err := core.ParseMoney(st.pendingAmount, currency)
	if err != nil || money.Minor < 0 {
		st.Step = wzStepAmount
		return b.wizardPrompt(tr, st, tr.t("wizard.err.amountForCurrency", currency))
	}
	st.HasAmount = true
	st.AmountMinor = money.Minor
	st.Currency = currency
	st.pendingAmount = ""
	st.Step = wzStepBillingDay
	return b.wizardPrompt(tr, st, "")
}

// prepareAssetStep loads assets for the asset step. A read failure is shown to
// the user and only Skip is offered, so the wizard never hides the problem.
func (b *Bot) prepareAssetStep(ctx context.Context, tr translator, st *wizardState) {
	st.AssetOptions = make(map[string]string)
	st.AssetLabels = make(map[string]string)
	st.AssetTokens = nil
	st.AssetNote = ""
	if b.store == nil {
		st.AssetNote = tr.t("wizard.assetUnavailable")
		return
	}
	assets, err := b.store.ListAssets(ctx, core.AssetFilter{Limit: 0})
	if err != nil {
		st.AssetNote = tr.t("wizard.assetLoadError")
		b.log.Warn("telegram: list assets for wizard failed", "error", err.Error())
		return
	}
	for i, asset := range assets {
		if i >= wizardAssetOptionLimit {
			break
		}
		token := fmt.Sprintf("a%d", i+1)
		st.AssetOptions[token] = asset.ID
		label := firstNonEmpty(ptrText(asset.Hostname), ptrText(asset.ProductModel), ptrText(asset.ProductID), asset.ID)
		if label == "" {
			label = tr.t("common.unknown")
		}
		st.AssetLabels[token] = label
		st.AssetTokens = append(st.AssetTokens, token)
	}
}

// prepareWizardStep refreshes step-specific data after navigation.
func (b *Bot) prepareWizardStep(ctx context.Context, tr translator, st *wizardState) {
	if st.Step == wzStepAsset {
		b.prepareAssetStep(ctx, tr, st)
	}
}

func validAmountDigits(s string) bool {
	if s == "" {
		return false
	}
	dot := false
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.' && !dot:
			dot = true
		default:
			return false
		}
	}
	return digits > 0
}

func previousWizardStep(st *wizardState) wizardStep {
	switch st.Step {
	case wzStepConfirm:
		return wzStepNotes
	case wzStepNotes:
		return wzStepPaymentURL
	case wzStepPaymentURL:
		return wzStepTimezone
	case wzStepTimezone:
		return wzStepReminderTime
	case wzStepReminderTime:
		return wzStepAsset
	case wzStepAsset:
		return wzStepBillingDay
	case wzStepBillingDay:
		return wzStepAmount
	case wzStepCurrency:
		return wzStepAmount
	case wzStepAmount:
		return wzStepProvider
	case wzStepProvider:
		return wzStepPeriod
	case wzStepPeriod:
		return wzStepTitle
	default:
		return wzStepTitle
	}
}

// wizardPrompt renders the current step, optionally prefixed with an error.
func (b *Bot) wizardPrompt(tr translator, st *wizardState, errMsg string) reply {
	if st.Step == wzStepConfirm {
		parts := formatManualPreviewAt(st, tr)
		if errMsg != "" {
			parts[0] = escape(errMsg) + "\n" + parts[0]
		}
		return reply{parts: parts, keyboard: b.wizardConfirmKeyboard(tr, st)}
	}
	text := wizardStepText(tr, st)
	if errMsg != "" {
		text = escape(errMsg) + "\n" + text
	}
	return reply{parts: []string{text}, keyboard: b.wizardKeyboard(tr, st)}
}

func wizardStepText(tr translator, st *wizardState) string {
	switch st.Step {
	case wzStepTitle:
		return tr.t("wizard.step.title")
	case wzStepPeriod:
		return tr.t("wizard.step.period")
	case wzStepProvider:
		return tr.t("wizard.step.provider")
	case wzStepAmount:
		return tr.t("wizard.step.amount")
	case wzStepCurrency:
		return tr.t("wizard.step.currency", escape(st.pendingAmount))
	case wzStepBillingDay:
		return tr.t("wizard.step.billingDay")
	case wzStepAsset:
		text := tr.t("wizard.step.asset")
		if st.AssetNote != "" {
			text = escape(st.AssetNote) + "\n" + text
		}
		return text
	case wzStepReminderTime:
		return tr.t("wizard.step.reminderTime", escape(st.ReminderTime))
	case wzStepTimezone:
		return tr.t("wizard.step.timezone", escape(st.Timezone))
	case wzStepPaymentURL:
		return tr.t("wizard.step.paymentURL")
	case wzStepNotes:
		return tr.t("wizard.step.notes")
	default:
		return tr.t("wizard.step.review")
	}
}

func (b *Bot) wizardKeyboard(tr translator, st *wizardState) *inlineKeyboard {
	rows := make([][]inlineButton, 0, 4)
	switch st.Step {
	case wzStepPeriod:
		rows = append(rows, []inlineButton{{
			Text:         tr.t("wizard.button.monthly"),
			CallbackData: callbackData(cbWizardValue, st.ID) + ":monthly",
		}})
	case wzStepCurrency:
		currencies := supportedCurrencies()
		for i := 0; i < len(currencies); i += 4 {
			end := i + 4
			if end > len(currencies) {
				end = len(currencies)
			}
			row := make([]inlineButton, 0, end-i)
			for _, currency := range currencies[i:end] {
				row = append(row, inlineButton{
					Text:         currency,
					CallbackData: callbackData(cbWizardValue, st.ID) + ":" + currency,
				})
			}
			rows = append(rows, row)
		}
	case wzStepAsset:
		for _, token := range st.AssetTokens {
			rows = append(rows, []inlineButton{{
				Text:         st.AssetLabels[token],
				CallbackData: callbackData(cbWizardValue, st.ID) + ":" + token,
			}})
		}
		rows = append(rows, []inlineButton{{
			Text:         tr.t("wizard.button.skip"),
			CallbackData: callbackData(cbWizardValue, st.ID) + ":skip",
		}})
	}
	nav := make([]inlineButton, 0, 2)
	if st.Step != wzStepTitle {
		nav = append(nav, inlineButton{Text: tr.t("wizard.button.back"), CallbackData: callbackData(cbWizardBack, st.ID)})
	}
	nav = append(nav, inlineButton{Text: tr.t("wizard.button.cancel"), CallbackData: callbackData(cbWizardCancel, st.ID)})
	rows = append(rows, nav)
	return &inlineKeyboard{InlineKeyboard: rows}
}

func (b *Bot) wizardConfirmKeyboard(tr translator, st *wizardState) *inlineKeyboard {
	return &inlineKeyboard{InlineKeyboard: [][]inlineButton{
		{{Text: tr.t("wizard.button.confirm"), CallbackData: callbackData(cbWizardAdvance, st.ID)}},
		{
			{Text: tr.t("wizard.button.back"), CallbackData: callbackData(cbWizardBack, st.ID)},
			{Text: tr.t("wizard.button.cancel"), CallbackData: callbackData(cbWizardCancel, st.ID)},
		},
	}}
}

// finishWizard creates or updates the plan from the wizard snapshot.
func (b *Bot) finishWizard(ctx context.Context, tr translator, st *wizardState) (reply, error) {
	input := core.ManualReminderInput{
		Title:        st.Title,
		ProviderName: st.ProviderName,
		AssetID:      st.AssetID,
		Description:  st.Description,
		BillingDay:   st.BillingDay,
		ReminderTime: st.ReminderTime,
		Timezone:     st.Timezone,
		PaymentURL:   st.PaymentURL,
		Notes:        st.Notes,
	}
	if st.HasAmount {
		input.Amount = &core.Money{Minor: st.AmountMinor, Currency: st.Currency}
	}
	if st.PlanID != "" {
		plan, err := b.manual.UpdatePlan(ctx, st.UserID, st.PlanID, input)
		if err != nil {
			return reply{}, err
		}
		return reply{parts: []string{tr.t("wizard.planUpdated", escape(plan.Title), plan.ID)}}, nil
	}
	plan, err := b.manual.CreatePlan(ctx, st.UserID, input)
	if err != nil {
		return reply{}, err
	}
	return reply{parts: []string{tr.t("wizard.planCreated", escape(plan.Title), plan.ID)}}, nil
}

// dropWizard removes the active wizard and reports whether one existed.
func (b *Bot) dropWizard(chatID, userID int64) bool {
	b.wizardMu.Lock()
	defer b.wizardMu.Unlock()
	key := wizardKey(chatID, userID)
	if b.wizards[key] == nil {
		return false
	}
	delete(b.wizards, key)
	return true
}

// handleWizardCallback handles the wz/wx/wb/wv namespace.
func (b *Bot) handleWizardCallback(ctx context.Context, cq *callbackQuery, action, id, value string) {
	tr := b.tr(ctx)
	if b.manual == nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.manualNotConfigured"), false)
		return
	}
	key := wizardKey(cq.Message.Chat.ID, cq.From.ID)
	b.wizardMu.Lock()
	st := b.wizards[key]
	if st == nil {
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, tr.t("wizard.notActive"), false)
		return
	}
	if b.wizardNow().After(st.ExpiresAt) {
		delete(b.wizards, key)
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, tr.t("wizard.expiredShort"), false)
		return
	}
	if st.ID != id {
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, tr.t("wizard.noLongerActive"), false)
		return
	}

	switch action {
	case cbWizardCancel:
		delete(b.wizards, key)
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, tr.t("wizard.cancelledShort"), false)
		b.editOrSend(ctx, cq, tr.t("manual.wizardCancelled"), nil)
	case cbWizardBack:
		st.Step = previousWizardStep(st)
		b.prepareWizardStep(ctx, tr, st)
		st.ExpiresAt = b.wizardNow().Add(b.wizardTTL())
		rep := b.wizardPrompt(tr, st, "")
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, tr.t("wizard.backShort"), false)
		b.editReply(ctx, cq, rep)
	case cbWizardValue:
		rep, answer, ok := b.applyWizardValue(tr, st, value)
		if !ok {
			b.wizardMu.Unlock()
			b.answerCallback(ctx, cq.ID, answer, false)
			return
		}
		st.ExpiresAt = b.wizardNow().Add(b.wizardTTL())
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, answer, false)
		b.editReply(ctx, cq, rep)
	case cbWizardAdvance:
		if st.Step != wzStepConfirm {
			b.wizardMu.Unlock()
			b.answerCallback(ctx, cq.ID, tr.t("wizard.useTextPrompt"), false)
			return
		}
		snapshot := *st
		b.wizardMu.Unlock()
		rep, err := b.finishWizard(ctx, tr, &snapshot)
		if err != nil {
			b.answerCallback(ctx, cq.ID, manualErrorText(tr, err), true)
			return
		}
		b.wizardMu.Lock()
		if current := b.wizards[key]; current != nil && current.ID == snapshot.ID {
			delete(b.wizards, key)
		}
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, tr.t("wizard.planSaved"), false)
		b.editReply(ctx, cq, rep)
	default:
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, tr.t("error.unknownAction"), false)
	}
}

// applyWizardValue handles a wv selection for the current step. It returns the
// next prompt, a callback answer and whether the value was valid.
func (b *Bot) applyWizardValue(tr translator, st *wizardState, value string) (reply, string, bool) {
	switch st.Step {
	case wzStepPeriod:
		if !strings.EqualFold(value, "monthly") {
			return reply{}, tr.t("wizard.onlyMonthly"), false
		}
		st.Period = "monthly"
		st.Step = wzStepProvider
		return b.wizardPrompt(tr, st, ""), tr.t("wizard.selectedMonthly"), true
	case wzStepCurrency:
		currency := strings.ToUpper(value)
		if _, ok := core.CurrencyDecimals(currency); !ok {
			return reply{}, tr.t("wizard.unsupportedCurrency"), false
		}
		return b.applyWizardCurrency(tr, st, currency), tr.t("wizard.selectedCurrency", currency), true
	case wzStepAsset:
		if strings.EqualFold(value, "skip") {
			st.AssetID = ""
			st.clearAssetOptions()
			st.Step = wzStepReminderTime
			return b.wizardPrompt(tr, st, ""), tr.t("wizard.assetSkipped"), true
		}
		assetID, ok := st.AssetOptions[value]
		if !ok {
			return reply{}, tr.t("wizard.unknownAsset"), false
		}
		st.AssetID = assetID
		st.clearAssetOptions()
		st.Step = wzStepReminderTime
		return b.wizardPrompt(tr, st, ""), tr.t("wizard.assetLinked"), true
	default:
		return reply{}, tr.t("wizard.noValue"), false
	}
}

func (st *wizardState) clearAssetOptions() {
	st.AssetOptions = nil
	st.AssetLabels = nil
	st.AssetTokens = nil
	st.AssetNote = ""
}
