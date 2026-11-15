package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"email-invoice-guard/internal/core"
)

const (
	manualMaxTitle    = 200
	manualMaxProvider = 100
	manualMaxNotes    = 2000
	manualMaxURL      = 2000
	// manualKeyboardOccurrenceLimit keeps inline keyboards well below
	// Telegram's 100-button ceiling; the text still lists every occurrence.
	manualKeyboardOccurrenceLimit = 20
)

func (b *Bot) requireManual(ctx context.Context) *reply {
	if b.manual != nil {
		return nil
	}
	return &reply{parts: []string{b.text(ctx, "error.manualNotConfigured")}}
}

func manualIDUsage(tr translator, usageKey string) reply {
	return reply{parts: []string{tr.t("manual.usagePrefix", tr.t(usageKey))}}
}

func (b *Bot) commandReminders(ctx context.Context) (reply, error) {
	tr := b.tr(ctx)
	if notConfigured := b.requireManual(ctx); notConfigured != nil {
		return *notConfigured, nil
	}
	plans, err := b.manual.ListPlans(ctx, core.ManualPlanFilter{Limit: listLimit})
	if err != nil {
		return reply{}, fmt.Errorf("list manual plans: %w", err)
	}
	if len(plans) == 0 {
		return reply{parts: []string{tr.t("manual.noPlans")}}, nil
	}
	lines := make([]string, 0, len(plans))
	rows := make([][]inlineButton, 0, len(plans))
	for i, plan := range plans {
		lines = append(lines, tr.t("manual.planLine",
			i+1, plan.Title, tr.planStatus(plan.Status), string(plan.NextDueDate), plan.ID))
		rows = append(rows, []inlineButton{{
			Text:         tr.t("manual.planButton", i+1),
			CallbackData: callbackData(cbManualView, plan.ID),
		}})
	}
	return renderList(tr.t("manual.title"), lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) commandReminder(ctx context.Context, args []string) (reply, error) {
	tr := b.tr(ctx)
	if notConfigured := b.requireManual(ctx); notConfigured != nil {
		return *notConfigured, nil
	}
	if len(args) != 1 || !validID(args[0]) {
		return manualIDUsage(tr, "manual.usage.reminder"), nil
	}
	plan, err := b.manual.GetPlan(ctx, args[0])
	if err != nil {
		return b.manualReadReply(tr, "get manual plan", err)
	}
	occurrences, err := b.pendingOccurrences(ctx, plan.ID)
	if err != nil {
		return reply{}, fmt.Errorf("list manual occurrences: %w", err)
	}
	return b.planReply(tr, plan, occurrences), nil
}

func (b *Bot) commandReminderAdd(ctx context.Context, userID, chatID int64) (reply, error) {
	if notConfigured := b.requireManual(ctx); notConfigured != nil {
		return *notConfigured, nil
	}
	return b.startWizard(ctx, userID, chatID, "")
}

func (b *Bot) commandReminderEdit(ctx context.Context, args []string, userID, chatID int64) (reply, error) {
	tr := b.tr(ctx)
	if notConfigured := b.requireManual(ctx); notConfigured != nil {
		return *notConfigured, nil
	}
	if len(args) != 1 || !validID(args[0]) {
		return manualIDUsage(tr, "manual.usage.edit"), nil
	}
	return b.startWizard(ctx, userID, chatID, args[0])
}

func (b *Bot) commandReminderPause(ctx context.Context, args []string, userID int64) (reply, error) {
	tr := b.tr(ctx)
	if notConfigured := b.requireManual(ctx); notConfigured != nil {
		return *notConfigured, nil
	}
	if len(args) != 1 || !validID(args[0]) {
		return manualIDUsage(tr, "manual.usage.pause"), nil
	}
	plan, err := b.manual.PausePlan(ctx, userID, args[0])
	if err != nil {
		return b.manualMutationReply(tr, "pause manual plan", err)
	}
	return reply{parts: []string{tr.t("manual.paused", escape(plan.Title))}}, nil
}

func (b *Bot) commandReminderResume(ctx context.Context, args []string, userID int64) (reply, error) {
	tr := b.tr(ctx)
	if notConfigured := b.requireManual(ctx); notConfigured != nil {
		return *notConfigured, nil
	}
	if len(args) != 1 || !validID(args[0]) {
		return manualIDUsage(tr, "manual.usage.resume"), nil
	}
	plan, err := b.manual.ResumePlan(ctx, userID, args[0])
	if err != nil {
		return b.manualMutationReply(tr, "resume manual plan", err)
	}
	return reply{parts: []string{tr.t("manual.resumed", escape(plan.Title), escape(string(plan.NextDueDate)))}}, nil
}

func (b *Bot) commandReminderDelete(ctx context.Context, args []string, userID int64) (reply, error) {
	tr := b.tr(ctx)
	if notConfigured := b.requireManual(ctx); notConfigured != nil {
		return *notConfigured, nil
	}
	if len(args) != 1 || !validID(args[0]) {
		return manualIDUsage(tr, "manual.usage.delete"), nil
	}
	plan, err := b.manual.CancelPlan(ctx, userID, args[0])
	if err != nil {
		return b.manualMutationReply(tr, "cancel manual plan", err)
	}
	return reply{parts: []string{tr.t("manual.cancelled", escape(plan.Title))}}, nil
}

func (b *Bot) commandReminderPaid(ctx context.Context, args []string, userID int64) (reply, error) {
	tr := b.tr(ctx)
	if notConfigured := b.requireManual(ctx); notConfigured != nil {
		return *notConfigured, nil
	}
	if len(args) != 1 || !validID(args[0]) {
		return manualIDUsage(tr, "manual.usage.paid"), nil
	}
	occ, err := b.manual.ConfirmOccurrence(ctx, userID, args[0])
	if err != nil {
		return b.manualMutationReply(tr, "confirm manual occurrence", err)
	}
	return reply{parts: []string{tr.t("manual.occurrencePaid", escape(string(occ.DueDate)))}}, nil
}

func (b *Bot) commandReminderPending(ctx context.Context) (reply, error) {
	tr := b.tr(ctx)
	if notConfigured := b.requireManual(ctx); notConfigured != nil {
		return *notConfigured, nil
	}
	occurrences, err := b.manual.ListOccurrences(ctx, core.ManualOccurrenceFilter{
		Status: core.ManualOccurrencePending,
		Limit:  0,
	})
	if err != nil {
		return reply{}, fmt.Errorf("list pending occurrences: %w", err)
	}
	sort.SliceStable(occurrences, func(i, j int) bool {
		if occurrences[i].DueDate != occurrences[j].DueDate {
			return occurrences[i].DueDate < occurrences[j].DueDate
		}
		return occurrences[i].ID < occurrences[j].ID
	})
	if len(occurrences) == 0 {
		return reply{parts: []string{tr.t("manual.pendingNone")}}, nil
	}
	titles := make(map[string]string)
	lines := make([]string, 0, len(occurrences))
	rows := make([][]inlineButton, 0, len(occurrences))
	for i, occ := range occurrences {
		title, ok := titles[occ.PlanID]
		if !ok {
			title = b.planTitle(ctx, occ.PlanID)
			titles[occ.PlanID] = title
		}
		lines = append(lines, tr.t("manual.pendingLine", i+1, title, string(occ.DueDate), manualAmountText(occ.Amount, tr), occ.ID))
		if i >= manualKeyboardOccurrenceLimit {
			continue
		}
		rows = append(rows, []inlineButton{
			{Text: tr.t("manual.markPaidButton", string(occ.DueDate)), CallbackData: callbackData(cbManualPaid, occ.ID)},
		})
	}
	return renderList(tr.t("manual.pendingTitle"), lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) commandReminderHistory(ctx context.Context, args []string) (reply, error) {
	tr := b.tr(ctx)
	if notConfigured := b.requireManual(ctx); notConfigured != nil {
		return *notConfigured, nil
	}
	if len(args) != 1 || !validID(args[0]) {
		return manualIDUsage(tr, "manual.usage.history"), nil
	}
	plan, err := b.manual.GetPlan(ctx, args[0])
	if err != nil {
		return b.manualReadReply(tr, "get manual plan", err)
	}
	events, err := b.manual.Events(ctx, plan.ID)
	if err != nil {
		return reply{}, fmt.Errorf("list manual events: %w", err)
	}
	return reply{parts: formatManualHistoryAt(plan, events, b.location(ctx), tr)}, nil
}

func (b *Bot) commandCancelWizard(ctx context.Context, chatID, userID int64) reply {
	tr := b.tr(ctx)
	if b.manual == nil {
		return reply{parts: []string{tr.t("error.manualNotConfigured")}}
	}
	if b.dropWizard(chatID, userID) {
		return reply{parts: []string{tr.t("manual.wizardCancelled")}}
	}
	return reply{parts: []string{tr.t("manual.noActiveWizard")}}
}

func (b *Bot) pendingOccurrences(ctx context.Context, planID string) ([]core.ManualOccurrence, error) {
	return b.manual.ListOccurrences(ctx, core.ManualOccurrenceFilter{
		PlanID: planID,
		Status: core.ManualOccurrencePending,
		Limit:  0,
	})
}

// planTitle resolves a plan title for a listing; a missing plan degrades to
// the plan ID rather than failing the whole command.
func (b *Bot) planTitle(ctx context.Context, planID string) string {
	plan, err := b.manual.GetPlan(ctx, planID)
	if err != nil {
		return planID
	}
	return plan.Title
}

func (b *Bot) planReply(tr translator, plan core.ManualPlan, occurrences []core.ManualOccurrence) reply {
	return reply{
		parts:    formatManualPlanAt(plan, occurrences, tr),
		keyboard: b.manualPlanKeyboard(tr, plan, occurrences),
	}
}

// manualReadReply maps a read failure to a user-facing message. Not-found is a
// normal answer; any other error is surfaced to the command error path.
func (b *Bot) manualReadReply(tr translator, action string, err error) (reply, error) {
	if errors.Is(err, core.ErrNotFound) {
		return reply{parts: []string{tr.t("error.reminderNotFound")}}, nil
	}
	return reply{}, fmt.Errorf("%s: %w", action, err)
}

func (b *Bot) manualMutationReply(tr translator, action string, err error) (reply, error) {
	switch {
	case errors.Is(err, core.ErrUnauthorized):
		return reply{parts: []string{tr.t("error.unauthorizedSentence")}}, nil
	case errors.Is(err, core.ErrConflict):
		return reply{parts: []string{tr.t("error.reminderConflict")}}, nil
	case errors.Is(err, core.ErrNotFound):
		return reply{parts: []string{tr.t("error.reminderNotFound")}}, nil
	case errors.Is(err, core.ErrInvalid):
		return reply{parts: []string{tr.t("error.reminderInvalid")}}, nil
	default:
		return reply{}, fmt.Errorf("%s: %w", action, err)
	}
}

func manualErrorText(tr translator, err error) string {
	switch {
	case errors.Is(err, core.ErrUnauthorized):
		return tr.t("error.unauthorized")
	case errors.Is(err, core.ErrConflict):
		return tr.t("error.reminderConflict")
	case errors.Is(err, core.ErrNotFound):
		return tr.t("error.reminderNotFound")
	case errors.Is(err, core.ErrInvalid):
		return tr.t("error.reminderInvalid")
	default:
		return tr.t("error.reminderUpdate")
	}
}

func (b *Bot) manualPlanKeyboard(tr translator, plan core.ManualPlan, occurrences []core.ManualOccurrence) *inlineKeyboard {
	rows := make([][]inlineButton, 0, len(occurrences)+2)
	for i, occ := range occurrences {
		if i >= manualKeyboardOccurrenceLimit {
			break
		}
		switch occ.Status {
		case core.ManualOccurrencePending:
			rows = append(rows, []inlineButton{
				{Text: tr.t("manual.markPaidButton", string(occ.DueDate)), CallbackData: callbackData(cbManualPaid, occ.ID)},
			})
		case core.ManualOccurrencePaid:
			rows = append(rows, []inlineButton{
				{Text: tr.t("manual.undoButton", string(occ.DueDate)), CallbackData: callbackData(cbManualUndo, occ.ID)},
			})
		}
	}
	rows = append(rows, []inlineButton{
		{Text: tr.t("manual.historyButton"), CallbackData: menuCallbackData(menuReminderHistory, plan.ID, "")},
	})
	return &inlineKeyboard{InlineKeyboard: rows}
}

func (b *Bot) manualSummaryKeyboard(tr translator, plan core.ManualPlan, occurrences []core.ManualOccurrence) *inlineKeyboard {
	rows := make([][]inlineButton, 0, len(occurrences)+2)
	for i, occ := range occurrences {
		if i >= manualKeyboardOccurrenceLimit {
			break
		}
		switch occ.Status {
		case core.ManualOccurrencePending:
			rows = append(rows, []inlineButton{
				{Text: tr.t("manual.markPaidButton", string(occ.DueDate)), CallbackData: callbackData(cbManualPaid, occ.ID)},
			})
		case core.ManualOccurrencePaid:
			rows = append(rows, []inlineButton{
				{Text: tr.t("manual.undoButton", string(occ.DueDate)), CallbackData: callbackData(cbManualUndo, occ.ID)},
			})
		}
	}
	rows = append(rows,
		[]inlineButton{{Text: tr.t("manual.viewPlanButton"), CallbackData: callbackData(cbManualView, plan.ID)}},
		[]inlineButton{{Text: tr.t("manual.historyButton"), CallbackData: callbackData(cbManualHistory, plan.ID)}},
	)
	return &inlineKeyboard{InlineKeyboard: rows}
}

// trustedHTTPSURL applies generic transport hygiene to an operator-declared
// payment link. No vendor policy exists for manual plans.
func trustedHTTPSURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return false
	}
	if u.Port() != "" && u.Port() != "443" {
		return false
	}
	return u.Fragment == ""
}

func manualAmountText(amount *core.Money, tr translator) string {
	if amount == nil {
		return tr.t("manual.amountNotFixed")
	}
	return core.FormatMoney(*amount)
}

func parseBillingDay(raw string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 || n > 31 {
		return 0, false
	}
	return n, true
}

func supportedCurrencies() []string {
	return []string{"USD", "EUR", "CNY", "HKD", "TWD", "GBP", "AUD", "CAD", "SGD", "JPY"}
}

// validReminderTime enforces a strict HH:MM shape; time.Parse alone accepts a
// single-digit hour.
func validReminderTime(raw string) bool {
	raw = strings.TrimSpace(raw)
	if len(raw) != 5 || raw[2] != ':' {
		return false
	}
	hh, err := strconv.Atoi(raw[:2])
	if err != nil || hh < 0 || hh > 23 {
		return false
	}
	mm, err := strconv.Atoi(raw[3:])
	return err == nil && mm >= 0 && mm <= 59
}
