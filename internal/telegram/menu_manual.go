package telegram

import (
	"context"
	"fmt"
	"sort"

	"email-invoice-guard/internal/core"
)

func (b *Bot) menuRemindersReply(ctx context.Context) (reply, error) {
	tr := b.tr(ctx)
	if b.manual == nil {
		return reply{
			parts:    []string{tr.t("error.manualNotConfigured")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, "", "")}},
		}, nil
	}
	plans, err := b.manual.ListPlans(ctx, core.ManualPlanFilter{Limit: listLimit})
	if err != nil {
		return reply{}, fmt.Errorf("list manual plans: %w", err)
	}
	lines := make([]string, 0, len(plans)+1)
	rows := make([][]inlineButton, 0, len(plans)+2)
	if len(plans) == 0 {
		lines = append(lines, tr.t("menu.reminders.empty"))
	}
	for i, plan := range plans {
		lines = append(lines, tr.t("manual.planLine",
			i+1, plan.Title, tr.planStatus(plan.Status), string(plan.NextDueDate), plan.ID))
		rows = append(rows, []inlineButton{{
			Text:         tr.t("manual.planButton", i+1),
			CallbackData: menuCallbackData(menuReminder, plan.ID, ""),
		}})
	}
	rows = append(rows, []inlineButton{
		{Text: tr.t("menu.button.newPlan"), CallbackData: menuCallbackData(menuReminderNew, "", "")},
		{Text: tr.t("menu.button.pending"), CallbackData: menuCallbackData(menuPending, "", "")},
	})
	rows = append(rows, menuNavRow(tr, "", ""))
	return renderList(tr.t("menu.reminders.title"), lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) menuReminderReply(ctx context.Context, planID string) (reply, error) {
	tr := b.tr(ctx)
	if b.manual == nil {
		return reply{
			parts:    []string{tr.t("error.manualNotConfigured")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuReminders, "")}},
		}, nil
	}
	plan, err := b.manual.GetPlan(ctx, planID)
	if err != nil {
		return reply{
			parts:    []string{tr.t("error.reminderNotFound")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuReminders, "")}},
		}, nil
	}
	occurrences, err := b.pendingOccurrences(ctx, planID)
	if err != nil {
		return reply{}, fmt.Errorf("list manual occurrences: %w", err)
	}
	rows := make([][]inlineButton, 0, len(occurrences)+3)
	if kb := b.manualPlanKeyboard(tr, plan, occurrences); kb != nil {
		rows = append(rows, kb.InlineKeyboard...)
	}
	actionRow := []inlineButton{
		{Text: tr.t("menu.button.edit"), CallbackData: menuCallbackData(menuReminderEdit, plan.ID, "")},
	}
	switch plan.Status {
	case core.ManualPlanActive:
		actionRow = append(actionRow, inlineButton{
			Text:         tr.t("menu.button.pause"),
			CallbackData: menuCallbackData(menuReminderPause, plan.ID, ""),
		})
	case core.ManualPlanPaused:
		actionRow = append(actionRow, inlineButton{
			Text:         tr.t("menu.button.resume"),
			CallbackData: menuCallbackData(menuReminderResume, plan.ID, ""),
		})
	}
	actionRow = append(actionRow, inlineButton{
		Text:         tr.t("menu.button.delete"),
		CallbackData: menuCallbackData(menuReminderDelete, plan.ID, ""),
	})
	rows = append(rows, actionRow)
	rows = append(rows, menuNavRow(tr, menuReminders, ""))
	return reply{
		parts:    formatManualPlanAt(plan, occurrences, tr),
		keyboard: &inlineKeyboard{InlineKeyboard: rows},
	}, nil
}

func (b *Bot) menuReminderHistoryReply(ctx context.Context, planID string) (reply, error) {
	tr := b.tr(ctx)
	if b.manual == nil {
		return reply{
			parts:    []string{tr.t("error.manualNotConfigured")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuReminders, "")}},
		}, nil
	}
	plan, err := b.manual.GetPlan(ctx, planID)
	if err != nil {
		return reply{
			parts:    []string{tr.t("error.reminderNotFound")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuReminders, "")}},
		}, nil
	}
	events, err := b.manual.Events(ctx, planID)
	if err != nil {
		return reply{}, fmt.Errorf("list manual events: %w", err)
	}
	return reply{
		parts:    formatManualHistoryAt(plan, events, b.location(ctx), tr),
		keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuReminder, plan.ID)}},
	}, nil
}

func (b *Bot) menuPendingReply(ctx context.Context) (reply, error) {
	tr := b.tr(ctx)
	if b.manual == nil {
		return reply{
			parts:    []string{tr.t("error.manualNotConfigured")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuReminders, "")}},
		}, nil
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
		return reply{
			parts:    []string{tr.t("manual.pendingNone")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuReminders, "")}},
		}, nil
	}
	titles := make(map[string]string)
	lines := make([]string, 0, len(occurrences))
	rows := make([][]inlineButton, 0, len(occurrences)+1)
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
	rows = append(rows, menuNavRow(tr, menuReminders, ""))
	return renderList(tr.t("manual.pendingTitle"), lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

func (b *Bot) menuReminderDeleteReply(ctx context.Context, planID string) (reply, error) {
	tr := b.tr(ctx)
	if b.manual == nil {
		return reply{
			parts:    []string{tr.t("error.manualNotConfigured")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuReminders, "")}},
		}, nil
	}
	plan, err := b.manual.GetPlan(ctx, planID)
	if err != nil {
		return reply{
			parts:    []string{tr.t("error.reminderNotFound")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuReminders, "")}},
		}, nil
	}
	rows := [][]inlineButton{
		{{Text: tr.t("menu.button.confirm"), CallbackData: menuCallbackData(menuReminderDeleteDo, plan.ID, "")}},
		menuNavRow(tr, menuReminder, plan.ID),
	}
	return reply{
		parts:    []string{tr.t("menu.reminderDelete.prompt", escape(plan.Title))},
		keyboard: &inlineKeyboard{InlineKeyboard: rows},
	}, nil
}

// menuReminderAction performs pause, resume or the confirmed cancel and
// re-renders the affected view.
func (b *Bot) menuReminderAction(ctx context.Context, cq *callbackQuery, view, planID string) (*reply, string, bool, error) {
	tr := b.tr(ctx)
	if b.manual == nil {
		return &reply{
			parts:    []string{tr.t("error.manualNotConfigured")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuReminders, "")}},
		}, "", false, nil
	}
	switch view {
	case menuReminderPause:
		plan, err := b.manual.PausePlan(ctx, cq.From.ID, planID)
		if err != nil {
			return nil, manualErrorText(tr, err), true, nil
		}
		rep, err := b.menuReminderReply(ctx, planID)
		if err != nil {
			return nil, "", false, err
		}
		return &rep, tr.t("menu.reminder.paused", plan.Title), false, nil
	case menuReminderResume:
		plan, err := b.manual.ResumePlan(ctx, cq.From.ID, planID)
		if err != nil {
			return nil, manualErrorText(tr, err), true, nil
		}
		rep, err := b.menuReminderReply(ctx, planID)
		if err != nil {
			return nil, "", false, err
		}
		return &rep, tr.t("menu.reminder.resumed", plan.Title), false, nil
	case menuReminderDeleteDo:
		plan, err := b.manual.CancelPlan(ctx, cq.From.ID, planID)
		if err != nil {
			return nil, manualErrorText(tr, err), true, nil
		}
		rep, err := b.menuRemindersReply(ctx)
		if err != nil {
			return nil, "", false, err
		}
		return &rep, tr.t("menu.planCancelled", plan.Title), false, nil
	default:
		return nil, tr.t("error.unknownAction"), false, nil
	}
}
