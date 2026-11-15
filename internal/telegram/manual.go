package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

const (
	manualNotConfigured = "Manual reminders are not configured."
	manualUsagePrefix   = "Usage: "
	manualMaxTitle      = 200
	manualMaxProvider   = 100
	manualMaxNotes      = 2000
	manualMaxURL        = 2000
	// manualKeyboardOccurrenceLimit keeps inline keyboards well below
	// Telegram's 100-button ceiling; the text still lists every occurrence.
	manualKeyboardOccurrenceLimit = 20
)

// manualDispatchReader is an optional read surface for dispatch history. The
// frozen service interface does not expose dispatches, so the concrete service
// or storage repository is used when available.
type manualDispatchReader interface {
	ListPlanDispatches(context.Context, string) ([]core.ManualDispatch, error)
	GetDispatch(context.Context, string) (core.ManualDispatch, error)
}

// dispatchReader prefers the injected manual service, then the store.
func (b *Bot) dispatchReader() manualDispatchReader {
	if b.manual != nil {
		if reader, ok := b.manual.(manualDispatchReader); ok {
			return reader
		}
	}
	if b.store != nil {
		if reader, ok := b.store.(manualDispatchReader); ok {
			return reader
		}
	}
	return nil
}

// manualRepo returns the optional repository used for operations the frozen
// service interface does not expose (per-occurrence cancellation).
func (b *Bot) manualRepo() core.ManualReminderRepository {
	if b.store == nil {
		return nil
	}
	repo, _ := b.store.(core.ManualReminderRepository)
	return repo
}

func (b *Bot) requireManual() *reply {
	if b.manual != nil {
		return nil
	}
	return &reply{parts: []string{manualNotConfigured}}
}

func manualIDUsage(usage string) reply {
	return reply{parts: []string{manualUsagePrefix + usage}}
}

func (b *Bot) commandReminders(ctx context.Context) (reply, error) {
	if notConfigured := b.requireManual(); notConfigured != nil {
		return *notConfigured, nil
	}
	plans, err := b.manual.ListPlans(ctx, core.ManualPlanFilter{Limit: listLimit})
	if err != nil {
		return reply{}, fmt.Errorf("list manual plans: %w", err)
	}
	if len(plans) == 0 {
		return reply{parts: []string{"Manual reminders: no plans. Use /reminder_add."}}, nil
	}
	lines := make([]string, 0, len(plans))
	rows := make([][]inlineButton, 0, len(plans))
	for i, plan := range plans {
		lines = append(lines, fmt.Sprintf("%d. %s — %s, next due %s (id=%s)",
			i+1, plan.Title, manualPlanStatusText(plan.Status), plan.NextDueDate, plan.ID))
		rows = append(rows, []inlineButton{{
			Text:         fmt.Sprintf("Plan %d", i+1),
			CallbackData: callbackData(cbManualView, plan.ID),
		}})
	}
	return renderList("Manual reminders", lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) commandReminder(ctx context.Context, args []string) (reply, error) {
	if notConfigured := b.requireManual(); notConfigured != nil {
		return *notConfigured, nil
	}
	if len(args) != 1 || !validID(args[0]) {
		return manualIDUsage("/reminder <id>"), nil
	}
	plan, err := b.manual.GetPlan(ctx, args[0])
	if err != nil {
		return b.manualReadReply("get manual plan", err)
	}
	occurrences, err := b.pendingOccurrences(ctx, plan.ID)
	if err != nil {
		return reply{}, fmt.Errorf("list manual occurrences: %w", err)
	}
	return b.planReply(plan, occurrences), nil
}

func (b *Bot) commandReminderAdd(ctx context.Context, userID, chatID int64) (reply, error) {
	if notConfigured := b.requireManual(); notConfigured != nil {
		return *notConfigured, nil
	}
	return b.startWizard(ctx, userID, chatID, "")
}

func (b *Bot) commandReminderEdit(ctx context.Context, args []string, userID, chatID int64) (reply, error) {
	if notConfigured := b.requireManual(); notConfigured != nil {
		return *notConfigured, nil
	}
	if len(args) != 1 || !validID(args[0]) {
		return manualIDUsage("/reminder_edit <id>"), nil
	}
	return b.startWizard(ctx, userID, chatID, args[0])
}

func (b *Bot) commandReminderPause(ctx context.Context, args []string, userID int64) (reply, error) {
	if notConfigured := b.requireManual(); notConfigured != nil {
		return *notConfigured, nil
	}
	if len(args) != 1 || !validID(args[0]) {
		return manualIDUsage("/reminder_pause <id>"), nil
	}
	plan, err := b.manual.PausePlan(ctx, userID, args[0])
	if err != nil {
		return b.manualMutationReply("pause manual plan", err)
	}
	return reply{parts: []string{"Plan paused: " + escape(plan.Title)}}, nil
}

func (b *Bot) commandReminderResume(ctx context.Context, args []string, userID int64) (reply, error) {
	if notConfigured := b.requireManual(); notConfigured != nil {
		return *notConfigured, nil
	}
	if len(args) != 1 || !validID(args[0]) {
		return manualIDUsage("/reminder_resume <id>"), nil
	}
	plan, err := b.manual.ResumePlan(ctx, userID, args[0])
	if err != nil {
		return b.manualMutationReply("resume manual plan", err)
	}
	return reply{parts: []string{"Plan resumed: " + escape(plan.Title) + ", next due " + escape(string(plan.NextDueDate))}}, nil
}

func (b *Bot) commandReminderDelete(ctx context.Context, args []string, userID int64) (reply, error) {
	if notConfigured := b.requireManual(); notConfigured != nil {
		return *notConfigured, nil
	}
	if len(args) != 1 || !validID(args[0]) {
		return manualIDUsage("/reminder_delete <id>"), nil
	}
	plan, err := b.manual.CancelPlan(ctx, userID, args[0])
	if err != nil {
		return b.manualMutationReply("cancel manual plan", err)
	}
	return reply{parts: []string{"Plan cancelled: " + escape(plan.Title)}}, nil
}

func (b *Bot) commandReminderPaid(ctx context.Context, args []string, userID int64) (reply, error) {
	if notConfigured := b.requireManual(); notConfigured != nil {
		return *notConfigured, nil
	}
	if len(args) != 1 || !validID(args[0]) {
		return manualIDUsage("/reminder_paid <occurrenceID>"), nil
	}
	occ, err := b.manual.ConfirmOccurrence(ctx, userID, args[0])
	if err != nil {
		return b.manualMutationReply("confirm manual occurrence", err)
	}
	return reply{parts: []string{fmt.Sprintf("Occurrence due %s marked as paid.", escape(string(occ.DueDate)))}}, nil
}

func (b *Bot) commandReminderPending(ctx context.Context) (reply, error) {
	if notConfigured := b.requireManual(); notConfigured != nil {
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
		return reply{parts: []string{"Pending manual occurrences: none."}}, nil
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
		lines = append(lines, fmt.Sprintf("%d. %s — due %s, %s (id=%s)", i+1, title, occ.DueDate, manualAmountText(occ.Amount), occ.ID))
		if i >= manualKeyboardOccurrenceLimit {
			continue
		}
		rows = append(rows, []inlineButton{
			{Text: "Paid " + string(occ.DueDate), CallbackData: callbackData(cbManualPaid, occ.ID)},
			{Text: "Cancel " + string(occ.DueDate), CallbackData: callbackData(cbManualCancel, occ.ID)},
		})
	}
	return renderList("Pending manual occurrences", lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) commandReminderHistory(ctx context.Context, args []string) (reply, error) {
	if notConfigured := b.requireManual(); notConfigured != nil {
		return *notConfigured, nil
	}
	if len(args) != 1 || !validID(args[0]) {
		return manualIDUsage("/reminder_history <id>"), nil
	}
	plan, err := b.manual.GetPlan(ctx, args[0])
	if err != nil {
		return b.manualReadReply("get manual plan", err)
	}
	events, err := b.manual.Events(ctx, plan.ID)
	if err != nil {
		return reply{}, fmt.Errorf("list manual events: %w", err)
	}
	var dispatches []core.ManualDispatch
	dispatchAvailable := false
	if reader := b.dispatchReader(); reader != nil {
		dispatchAvailable = true
		dispatches, err = reader.ListPlanDispatches(ctx, plan.ID)
		if err != nil {
			return reply{}, fmt.Errorf("list manual dispatches: %w", err)
		}
	}
	return reply{
		parts:    formatManualHistoryAt(plan, events, dispatches, dispatchAvailable, b.location(ctx)),
		keyboard: manualHistoryKeyboard(dispatches),
	}, nil
}

func (b *Bot) commandCancelWizard(chatID, userID int64) reply {
	if b.manual == nil {
		return reply{parts: []string{manualNotConfigured}}
	}
	if b.dropWizard(chatID, userID) {
		return reply{parts: []string{"Wizard cancelled."}}
	}
	return reply{parts: []string{"No active wizard."}}
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

func (b *Bot) planReply(plan core.ManualPlan, occurrences []core.ManualOccurrence) reply {
	return reply{
		parts:    formatManualPlanAt(plan, occurrences),
		keyboard: b.manualPlanKeyboard(plan, occurrences),
	}
}

// manualReadReply maps a read failure to a user-facing message. Not-found is a
// normal answer; any other error is surfaced to the command error path.
func (b *Bot) manualReadReply(action string, err error) (reply, error) {
	if errors.Is(err, core.ErrNotFound) {
		return reply{parts: []string{"Reminder not found."}}, nil
	}
	return reply{}, fmt.Errorf("%s: %w", action, err)
}

func (b *Bot) manualMutationReply(action string, err error) (reply, error) {
	switch {
	case errors.Is(err, core.ErrUnauthorized):
		return reply{parts: []string{"Not authorized."}}, nil
	case errors.Is(err, core.ErrConflict):
		return reply{parts: []string{"Cannot change this reminder in its current state."}}, nil
	case errors.Is(err, core.ErrNotFound):
		return reply{parts: []string{"Reminder not found."}}, nil
	case errors.Is(err, core.ErrInvalid):
		return reply{parts: []string{"Invalid reminder details."}}, nil
	default:
		return reply{}, fmt.Errorf("%s: %w", action, err)
	}
}

func manualErrorText(err error) string {
	switch {
	case errors.Is(err, core.ErrUnauthorized):
		return "Not authorized"
	case errors.Is(err, core.ErrConflict):
		return "Cannot change this reminder in its current state"
	case errors.Is(err, core.ErrNotFound):
		return "Reminder not found"
	case errors.Is(err, core.ErrInvalid):
		return "Invalid reminder details"
	default:
		return "Reminder update failed"
	}
}

func (b *Bot) manualPlanKeyboard(plan core.ManualPlan, occurrences []core.ManualOccurrence) *inlineKeyboard {
	rows := make([][]inlineButton, 0, len(occurrences)+2)
	for i, occ := range occurrences {
		if i >= manualKeyboardOccurrenceLimit {
			break
		}
		switch occ.Status {
		case core.ManualOccurrencePending:
			rows = append(rows, []inlineButton{
				{Text: "Paid " + string(occ.DueDate), CallbackData: callbackData(cbManualPaid, occ.ID)},
				{Text: "Cancel " + string(occ.DueDate), CallbackData: callbackData(cbManualCancel, occ.ID)},
			})
		case core.ManualOccurrencePaid:
			rows = append(rows, []inlineButton{
				{Text: "Undo " + string(occ.DueDate), CallbackData: callbackData(cbManualUndo, occ.ID)},
			})
		}
	}
	rows = append(rows, []inlineButton{
		{Text: "History", CallbackData: callbackData(cbManualHistory, plan.ID)},
	})
	return &inlineKeyboard{InlineKeyboard: rows}
}

func (b *Bot) manualSummaryKeyboard(plan core.ManualPlan, occurrences []core.ManualOccurrence) *inlineKeyboard {
	rows := make([][]inlineButton, 0, len(occurrences)+2)
	for i, occ := range occurrences {
		if i >= manualKeyboardOccurrenceLimit {
			break
		}
		switch occ.Status {
		case core.ManualOccurrencePending:
			rows = append(rows, []inlineButton{
				{Text: "Paid " + string(occ.DueDate), CallbackData: callbackData(cbManualPaid, occ.ID)},
				{Text: "Cancel " + string(occ.DueDate), CallbackData: callbackData(cbManualCancel, occ.ID)},
			})
		case core.ManualOccurrencePaid:
			rows = append(rows, []inlineButton{
				{Text: "Undo " + string(occ.DueDate), CallbackData: callbackData(cbManualUndo, occ.ID)},
			})
		}
	}
	rows = append(rows,
		[]inlineButton{{Text: "View plan", CallbackData: callbackData(cbManualView, plan.ID)}},
		[]inlineButton{{Text: "History", CallbackData: callbackData(cbManualHistory, plan.ID)}},
	)
	return &inlineKeyboard{InlineKeyboard: rows}
}

// manualHistoryKeyboard exposes dispatch detail for each listed dispatch.
func manualHistoryKeyboard(dispatches []core.ManualDispatch) *inlineKeyboard {
	if len(dispatches) == 0 {
		return nil
	}
	rows := make([][]inlineButton, 0, len(dispatches))
	for i, dispatch := range dispatches {
		if i >= manualKeyboardOccurrenceLimit {
			break
		}
		rows = append(rows, []inlineButton{{
			Text:         "Dispatch " + string(dispatch.LocalDate),
			CallbackData: callbackData(cbManualDispatch, dispatch.ID),
		}})
	}
	if len(rows) == 0 {
		return nil
	}
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

func manualAmountText(amount *core.Money) string {
	if amount == nil {
		return "amount not fixed"
	}
	return core.FormatMoney(*amount)
}

func manualPlanStatusText(status core.ManualPlanStatus) string {
	switch status {
	case core.ManualPlanActive:
		return "ACTIVE"
	case core.ManualPlanPaused:
		return "PAUSED"
	case core.ManualPlanCancelled:
		return "CANCELLED"
	default:
		return string(status)
	}
}

func manualOccurrenceStatusText(status core.ManualOccurrenceStatus) string {
	switch status {
	case core.ManualOccurrencePending:
		return "PENDING (unpaid)"
	case core.ManualOccurrencePaid:
		return "PAID"
	case core.ManualOccurrenceCancelled:
		return "CANCELLED"
	default:
		return string(status)
	}
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

func validReminderTime(raw string) bool {
	_, err := time.Parse("15:04", strings.TrimSpace(raw))
	return err == nil
}
