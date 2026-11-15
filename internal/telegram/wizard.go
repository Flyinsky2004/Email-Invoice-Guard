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
			return b.manualReadReply("get manual plan", err)
		}
		st.prefill(plan)
		st.Step = wzStepConfirm
	}
	b.wizardMu.Lock()
	b.wizards[wizardKey(chatID, userID)] = st
	b.wizardMu.Unlock()
	return b.wizardPrompt(st, ""), nil
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
		return reply{parts: []string{"Wizard expired. Start again with /reminder_add."}}, true
	}
	rep := b.wizardApplyText(ctx, st, strings.TrimSpace(m.Text))
	st.ExpiresAt = b.wizardNow().Add(b.wizardTTL())
	b.wizardMu.Unlock()
	return rep, true
}

// wizardApplyText validates one text answer, stores it and returns the next
// prompt. A validation failure keeps the current step.
func (b *Bot) wizardApplyText(ctx context.Context, st *wizardState, text string) reply {
	switch st.Step {
	case wzStepTitle:
		if text == "" || utf8.RuneCountInString(text) > manualMaxTitle {
			return b.wizardPrompt(st, "Title must be 1-200 characters.")
		}
		st.Title = text
		st.Step = wzStepPeriod
	case wzStepPeriod:
		if !strings.EqualFold(text, "monthly") {
			return b.wizardPrompt(st, "Select Monthly to continue; v1 supports monthly plans only.")
		}
		st.Period = "monthly"
		st.Step = wzStepProvider
	case wzStepProvider:
		if text == "-" {
			text = ""
		}
		if utf8.RuneCountInString(text) > manualMaxProvider {
			return b.wizardPrompt(st, "Provider name must be at most 100 characters.")
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
				return b.wizardPrompt(st, "Send an amount like 18.00 USD, or - for no fixed amount.")
			}
			st.pendingAmount = fields[0]
			st.Step = wzStepCurrency
		case 2:
			currency := strings.ToUpper(fields[1])
			money, err := core.ParseMoney(fields[0], currency)
			if err != nil || money.Minor < 0 {
				return b.wizardPrompt(st, "Invalid amount or unsupported currency.")
			}
			st.HasAmount = true
			st.AmountMinor = money.Minor
			st.Currency = currency
			st.pendingAmount = ""
			st.Step = wzStepBillingDay
		default:
			return b.wizardPrompt(st, "Send an amount like 18.00 USD, or - for no fixed amount.")
		}
	case wzStepCurrency:
		currency := strings.ToUpper(strings.TrimSpace(text))
		if _, ok := core.CurrencyDecimals(currency); !ok {
			return b.wizardPrompt(st, "Unsupported currency. Select one of the buttons.")
		}
		return b.applyWizardCurrency(st, currency)
	case wzStepBillingDay:
		day, ok := parseBillingDay(text)
		if !ok {
			return b.wizardPrompt(st, "Billing day must be a number from 1 to 31.")
		}
		st.BillingDay = day
		st.Step = wzStepAsset
		b.prepareAssetStep(ctx, st)
	case wzStepAsset:
		return b.wizardPrompt(st, "Select an asset or Skip.")
	case wzStepReminderTime:
		if text != "-" {
			if !validReminderTime(text) {
				return b.wizardPrompt(st, "Reminder time must be HH:MM in 24-hour form.")
			}
			st.ReminderTime = text
		}
		st.Step = wzStepTimezone
	case wzStepTimezone:
		if text != "-" {
			if _, err := time.LoadLocation(text); err != nil {
				return b.wizardPrompt(st, "Invalid timezone. Use an IANA name such as Asia/Taipei.")
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
			return b.wizardPrompt(st, "Payment URL must be an https URL without credentials, a non-443 port or a fragment.")
		}
		st.PaymentURL = text
		st.Step = wzStepNotes
	case wzStepNotes:
		if text == "-" {
			text = ""
		}
		if utf8.RuneCountInString(text) > manualMaxNotes {
			return b.wizardPrompt(st, "Notes must be at most 2000 characters.")
		}
		st.Notes = text
		st.Step = wzStepConfirm
	case wzStepConfirm:
		return b.wizardPrompt(st, "Press Confirm, Back or Cancel.")
	}
	return b.wizardPrompt(st, "")
}

// applyWizardCurrency completes the amount step after a currency selection.
func (b *Bot) applyWizardCurrency(st *wizardState, currency string) reply {
	if st.pendingAmount == "" {
		st.Step = wzStepAmount
		return b.wizardPrompt(st, "Send an amount first.")
	}
	money, err := core.ParseMoney(st.pendingAmount, currency)
	if err != nil || money.Minor < 0 {
		st.Step = wzStepAmount
		return b.wizardPrompt(st, "Invalid amount for "+currency+".")
	}
	st.HasAmount = true
	st.AmountMinor = money.Minor
	st.Currency = currency
	st.pendingAmount = ""
	st.Step = wzStepBillingDay
	return b.wizardPrompt(st, "")
}

// prepareAssetStep loads assets for the asset step. A read failure is shown to
// the user and only Skip is offered, so the wizard never hides the problem.
func (b *Bot) prepareAssetStep(ctx context.Context, st *wizardState) {
	st.AssetOptions = make(map[string]string)
	st.AssetLabels = make(map[string]string)
	st.AssetTokens = nil
	st.AssetNote = ""
	if b.store == nil {
		st.AssetNote = "Assets are unavailable; press Skip."
		return
	}
	assets, err := b.store.ListAssets(ctx, core.AssetFilter{Limit: 0})
	if err != nil {
		st.AssetNote = "Could not load assets; press Skip."
		b.log.Warn("telegram: list assets for wizard failed", "error", err.Error())
		return
	}
	for i, asset := range assets {
		if i >= wizardAssetOptionLimit {
			break
		}
		token := fmt.Sprintf("a%d", i+1)
		st.AssetOptions[token] = asset.ID
		st.AssetLabels[token] = firstNonEmpty(ptrText(asset.Hostname), ptrText(asset.ProductModel), ptrText(asset.ProductID), asset.ID)
		st.AssetTokens = append(st.AssetTokens, token)
	}
}

// prepareWizardStep refreshes step-specific data after navigation.
func (b *Bot) prepareWizardStep(ctx context.Context, st *wizardState) {
	if st.Step == wzStepAsset {
		b.prepareAssetStep(ctx, st)
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
func (b *Bot) wizardPrompt(st *wizardState, errMsg string) reply {
	if st.Step == wzStepConfirm {
		parts := formatManualPreviewAt(st)
		if errMsg != "" {
			parts[0] = escape(errMsg) + "\n" + parts[0]
		}
		return reply{parts: parts, keyboard: b.wizardConfirmKeyboard(st)}
	}
	text := wizardStepText(st)
	if errMsg != "" {
		text = escape(errMsg) + "\n" + text
	}
	return reply{parts: []string{text}, keyboard: b.wizardKeyboard(st)}
}

func wizardStepText(st *wizardState) string {
	switch st.Step {
	case wzStepTitle:
		return "Step 1/10 — send the plan title."
	case wzStepPeriod:
		return "Step 2/10 — select the billing period. v1 supports monthly plans only."
	case wzStepProvider:
		return "Step 3/10 — send the provider name, or - to skip."
	case wzStepAmount:
		return "Step 4/10 — send the amount like 18.00 USD, or - for no fixed amount."
	case wzStepCurrency:
		return "Step 4/10 — select the currency for " + escape(st.pendingAmount) + "."
	case wzStepBillingDay:
		return "Step 5/10 — send the billing day (1-31)."
	case wzStepAsset:
		text := "Step 6/10 — select an asset to link, or Skip."
		if st.AssetNote != "" {
			text = escape(st.AssetNote) + "\n" + text
		}
		return text
	case wzStepReminderTime:
		return "Step 7/10 — send the reminder time HH:MM (default " + escape(st.ReminderTime) + "), or - to keep it."
	case wzStepTimezone:
		return "Step 8/10 — send the IANA timezone (default " + escape(st.Timezone) + "), or - to keep it."
	case wzStepPaymentURL:
		return "Step 9/10 — send an https payment URL, or - to skip."
	case wzStepNotes:
		return "Step 10/10 — send notes, or - to skip."
	default:
		return "Review the plan."
	}
}

func (b *Bot) wizardKeyboard(st *wizardState) *inlineKeyboard {
	rows := make([][]inlineButton, 0, 4)
	switch st.Step {
	case wzStepPeriod:
		rows = append(rows, []inlineButton{{
			Text:         "Monthly (v1 only)",
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
			Text:         "Skip",
			CallbackData: callbackData(cbWizardValue, st.ID) + ":skip",
		}})
	}
	nav := make([]inlineButton, 0, 2)
	if st.Step != wzStepTitle {
		nav = append(nav, inlineButton{Text: "Back", CallbackData: callbackData(cbWizardBack, st.ID)})
	}
	nav = append(nav, inlineButton{Text: "Cancel", CallbackData: callbackData(cbWizardCancel, st.ID)})
	rows = append(rows, nav)
	return &inlineKeyboard{InlineKeyboard: rows}
}

func (b *Bot) wizardConfirmKeyboard(st *wizardState) *inlineKeyboard {
	return &inlineKeyboard{InlineKeyboard: [][]inlineButton{
		{{Text: "Confirm", CallbackData: callbackData(cbWizardAdvance, st.ID)}},
		{
			{Text: "Back", CallbackData: callbackData(cbWizardBack, st.ID)},
			{Text: "Cancel", CallbackData: callbackData(cbWizardCancel, st.ID)},
		},
	}}
}

// finishWizard creates or updates the plan from the wizard snapshot.
func (b *Bot) finishWizard(ctx context.Context, st *wizardState) (reply, error) {
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
		return reply{parts: []string{"Plan updated: " + escape(plan.Title) + " (id=" + plan.ID + ")"}}, nil
	}
	plan, err := b.manual.CreatePlan(ctx, st.UserID, input)
	if err != nil {
		return reply{}, err
	}
	return reply{parts: []string{"Plan created: " + escape(plan.Title) + " (id=" + plan.ID + ")"}}, nil
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
	if b.manual == nil {
		b.answerCallback(ctx, cq.ID, manualNotConfigured, false)
		return
	}
	key := wizardKey(cq.Message.Chat.ID, cq.From.ID)
	b.wizardMu.Lock()
	st := b.wizards[key]
	if st == nil {
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, "Wizard is not active", false)
		return
	}
	if b.wizardNow().After(st.ExpiresAt) {
		delete(b.wizards, key)
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, "Wizard expired", false)
		return
	}
	if st.ID != id {
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, "This wizard is no longer active", false)
		return
	}

	switch action {
	case cbWizardCancel:
		delete(b.wizards, key)
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, "Wizard cancelled", false)
		b.editOrSend(ctx, cq, "Wizard cancelled.", nil)
	case cbWizardBack:
		st.Step = previousWizardStep(st)
		b.prepareWizardStep(ctx, st)
		st.ExpiresAt = b.wizardNow().Add(b.wizardTTL())
		rep := b.wizardPrompt(st, "")
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, "Back", false)
		b.editReply(ctx, cq, rep)
	case cbWizardValue:
		rep, answer, ok := b.applyWizardValue(st, value)
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
			b.answerCallback(ctx, cq.ID, "Use the text prompt to continue", false)
			return
		}
		snapshot := *st
		b.wizardMu.Unlock()
		rep, err := b.finishWizard(ctx, &snapshot)
		if err != nil {
			b.answerCallback(ctx, cq.ID, manualErrorText(err), true)
			return
		}
		b.wizardMu.Lock()
		if current := b.wizards[key]; current != nil && current.ID == snapshot.ID {
			delete(b.wizards, key)
		}
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, "Plan saved", false)
		b.editReply(ctx, cq, rep)
	default:
		b.wizardMu.Unlock()
		b.answerCallback(ctx, cq.ID, "Unknown action", false)
	}
}

// applyWizardValue handles a wv selection for the current step. It returns the
// next prompt, a callback answer and whether the value was valid.
func (b *Bot) applyWizardValue(st *wizardState, value string) (reply, string, bool) {
	switch st.Step {
	case wzStepPeriod:
		if !strings.EqualFold(value, "monthly") {
			return reply{}, "Only monthly is supported", false
		}
		st.Period = "monthly"
		st.Step = wzStepProvider
		return b.wizardPrompt(st, ""), "Selected monthly", true
	case wzStepCurrency:
		currency := strings.ToUpper(value)
		if _, ok := core.CurrencyDecimals(currency); !ok {
			return reply{}, "Unsupported currency", false
		}
		return b.applyWizardCurrency(st, currency), "Selected " + currency, true
	case wzStepAsset:
		if strings.EqualFold(value, "skip") {
			st.AssetID = ""
			st.clearAssetOptions()
			st.Step = wzStepReminderTime
			return b.wizardPrompt(st, ""), "Asset skipped", true
		}
		assetID, ok := st.AssetOptions[value]
		if !ok {
			return reply{}, "Unknown asset", false
		}
		st.AssetID = assetID
		st.clearAssetOptions()
		st.Step = wzStepReminderTime
		return b.wizardPrompt(st, ""), "Asset linked", true
	default:
		return reply{}, "No value to select here", false
	}
}

func (st *wizardState) clearAssetOptions() {
	st.AssetOptions = nil
	st.AssetLabels = nil
	st.AssetTokens = nil
	st.AssetNote = ""
}
