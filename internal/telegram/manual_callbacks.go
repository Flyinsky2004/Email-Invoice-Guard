package telegram

import (
	"context"

	"email-invoice-guard/internal/core"
)

// callbackManualPaid only shows a two-stage confirmation prompt. A single
// misclick must not mark a period paid; callbackManualConfirm performs it.
func (b *Bot) callbackManualPaid(ctx context.Context, cq *callbackQuery, occurrenceID string) {
	tr := b.tr(ctx)
	if b.manual == nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.manualNotConfigured"), false)
		return
	}
	occ, err := b.manual.GetOccurrence(ctx, occurrenceID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, manualErrorText(tr, err), false)
		return
	}
	if occ.Status != core.ManualOccurrencePending {
		b.answerCallback(ctx, cq.ID, tr.t("manual.occNotPending"), false)
		return
	}
	plan, err := b.manual.GetPlan(ctx, occ.PlanID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, manualErrorText(tr, err), false)
		return
	}
	text := tr.t("manual.confirmPrompt",
		escape(plan.Title), escape(string(occ.DueDate)), escape(manualAmountText(occurrenceAmount(plan, occ), tr)))
	markup := &inlineKeyboard{InlineKeyboard: [][]inlineButton{
		{{Text: tr.t("callback.confirmPaid"), CallbackData: callbackData(cbManualConfirm, occ.ID)}},
		{{Text: tr.t("callback.cancel"), CallbackData: callbackData(cbManualView, plan.ID)}},
	}}
	b.answerCallback(ctx, cq.ID, tr.t("callback.confirmPayment"), false)
	b.editOrSend(ctx, cq, text, markup)
}

// callbackManualConfirm performs the confirmed transition.
func (b *Bot) callbackManualConfirm(ctx context.Context, cq *callbackQuery, occurrenceID string) {
	tr := b.tr(ctx)
	if b.manual == nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.manualNotConfigured"), false)
		return
	}
	occ, err := b.manual.ConfirmOccurrence(ctx, cq.From.ID, occurrenceID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, manualErrorText(tr, err), true)
		return
	}
	b.answerCallback(ctx, cq.ID, tr.t("callback.markedPaid"), false)
	b.refreshManualMessage(ctx, cq, occ.PlanID)
}

func (b *Bot) callbackManualUndo(ctx context.Context, cq *callbackQuery, occurrenceID string) {
	tr := b.tr(ctx)
	if b.manual == nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.manualNotConfigured"), false)
		return
	}
	occ, err := b.manual.RevokeOccurrence(ctx, cq.From.ID, occurrenceID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, manualErrorText(tr, err), true)
		return
	}
	b.answerCallback(ctx, cq.ID, tr.t("callback.restoredPending"), false)
	b.refreshManualMessage(ctx, cq, occ.PlanID)
}

func (b *Bot) callbackManualView(ctx context.Context, cq *callbackQuery, planID string) {
	tr := b.tr(ctx)
	if b.manual == nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.manualNotConfigured"), false)
		return
	}
	plan, err := b.manual.GetPlan(ctx, planID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, manualErrorText(tr, err), false)
		return
	}
	occurrences, err := b.pendingOccurrences(ctx, planID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.planLookup"), true)
		b.log.Warn("telegram: list manual occurrences failed", "plan", planID, "error", err.Error())
		return
	}
	b.answerCallback(ctx, cq.ID, tr.t("manual.openingPlan"), false)
	b.editReply(ctx, cq, b.planReply(tr, plan, occurrences))
}

func (b *Bot) callbackManualHistory(ctx context.Context, cq *callbackQuery, planID string) {
	tr := b.tr(ctx)
	if b.manual == nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.manualNotConfigured"), false)
		return
	}
	plan, err := b.manual.GetPlan(ctx, planID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, manualErrorText(tr, err), false)
		return
	}
	events, err := b.manual.Events(ctx, planID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, tr.t("error.historyLookup"), true)
		b.log.Warn("telegram: list manual events failed", "plan", planID, "error", err.Error())
		return
	}
	b.answerCallback(ctx, cq.ID, tr.t("manual.openingHistory"), false)
	b.sendManualReply(ctx, cq, reply{parts: formatManualHistoryAt(plan, events, b.location(ctx), tr)})
}

// refreshManualMessage re-renders the plan after a successful transition. An
// edit failure is transient and never rolls back the persisted state.
func (b *Bot) refreshManualMessage(ctx context.Context, cq *callbackQuery, planID string) {
	tr := b.tr(ctx)
	plan, err := b.manual.GetPlan(ctx, planID)
	if err != nil {
		b.log.Warn("telegram: refresh manual plan failed", "plan", planID, "error", err.Error())
		return
	}
	occurrences, err := b.pendingOccurrences(ctx, planID)
	if err != nil {
		b.log.Warn("telegram: refresh manual occurrences failed", "plan", planID, "error", err.Error())
		return
	}
	b.editReply(ctx, cq, b.planReply(tr, plan, occurrences))
}

// sendManualReply sends a fresh message to the callback chat.
func (b *Bot) sendManualReply(ctx context.Context, cq *callbackQuery, rep reply) {
	chatID := b.cfg.Telegram.ChatID
	if cq.Message != nil {
		chatID = cq.Message.Chat.ID
	}
	if err := b.sendParts(ctx, chatID, rep.parts, rep.keyboard); err != nil {
		b.log.Warn("telegram: send manual reply failed", "error", err.Error())
	}
}

// editReply edits the callback message to the first part and sends any
// remaining parts as new messages. The keyboard is attached once, to the last
// part.
func (b *Bot) editReply(ctx context.Context, cq *callbackQuery, rep reply) {
	if len(rep.parts) == 0 {
		return
	}
	if cq.Message != nil {
		if err := b.editMessageReply(ctx, cq.Message.Chat.ID, cq.Message.MessageID, rep); err == nil {
			return
		} else {
			b.log.Warn("telegram: editMessageText failed", "error", err.Error())
		}
	}
	b.sendManualReply(ctx, cq, rep)
}
