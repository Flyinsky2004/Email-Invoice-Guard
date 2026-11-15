package telegram

import (
	"context"

	"email-invoice-guard/internal/core"
)

// callbackManualPaid only shows a two-stage confirmation prompt. A single
// misclick must not mark a period paid; callbackManualConfirm performs it.
func (b *Bot) callbackManualPaid(ctx context.Context, cq *callbackQuery, occurrenceID string) {
	if b.manual == nil {
		b.answerCallback(ctx, cq.ID, manualNotConfigured, false)
		return
	}
	occ, err := b.manual.GetOccurrence(ctx, occurrenceID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, manualErrorText(err), false)
		return
	}
	if occ.Status != core.ManualOccurrencePending {
		b.answerCallback(ctx, cq.ID, "Occurrence is not pending confirmation", false)
		return
	}
	plan, err := b.manual.GetPlan(ctx, occ.PlanID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, manualErrorText(err), false)
		return
	}
	text := "Confirm manual payment for " + escape(plan.Title) + " due " + escape(string(occ.DueDate)) +
		" (" + escape(manualAmountText(occurrenceAmount(plan, occ))) + ")?\n" +
		"Only press Confirm if you have actually paid this occurrence."
	markup := &inlineKeyboard{InlineKeyboard: [][]inlineButton{
		{{Text: "Confirm paid", CallbackData: callbackData(cbManualConfirm, occ.ID)}},
		{{Text: "Cancel", CallbackData: callbackData(cbManualView, plan.ID)}},
	}}
	b.answerCallback(ctx, cq.ID, "Confirm payment", false)
	b.editOrSend(ctx, cq, text, markup)
}

// callbackManualConfirm performs the confirmed transition.
func (b *Bot) callbackManualConfirm(ctx context.Context, cq *callbackQuery, occurrenceID string) {
	if b.manual == nil {
		b.answerCallback(ctx, cq.ID, manualNotConfigured, false)
		return
	}
	occ, err := b.manual.ConfirmOccurrence(ctx, cq.From.ID, occurrenceID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, manualErrorText(err), true)
		return
	}
	b.answerCallback(ctx, cq.ID, "Marked as paid", false)
	b.refreshManualMessage(ctx, cq, occ.PlanID)
}

func (b *Bot) callbackManualUndo(ctx context.Context, cq *callbackQuery, occurrenceID string) {
	if b.manual == nil {
		b.answerCallback(ctx, cq.ID, manualNotConfigured, false)
		return
	}
	occ, err := b.manual.RevokeOccurrence(ctx, cq.From.ID, occurrenceID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, manualErrorText(err), true)
		return
	}
	b.answerCallback(ctx, cq.ID, "Restored to pending", false)
	b.refreshManualMessage(ctx, cq, occ.PlanID)
}

func (b *Bot) callbackManualView(ctx context.Context, cq *callbackQuery, planID string) {
	if b.manual == nil {
		b.answerCallback(ctx, cq.ID, manualNotConfigured, false)
		return
	}
	plan, err := b.manual.GetPlan(ctx, planID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, manualErrorText(err), false)
		return
	}
	occurrences, err := b.pendingOccurrences(ctx, planID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, "Plan lookup failed", true)
		b.log.Warn("telegram: list manual occurrences failed", "plan", planID, "error", err.Error())
		return
	}
	b.answerCallback(ctx, cq.ID, "Opening plan", false)
	b.editReply(ctx, cq, b.planReply(plan, occurrences))
}

func (b *Bot) callbackManualHistory(ctx context.Context, cq *callbackQuery, planID string) {
	if b.manual == nil {
		b.answerCallback(ctx, cq.ID, manualNotConfigured, false)
		return
	}
	plan, err := b.manual.GetPlan(ctx, planID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, manualErrorText(err), false)
		return
	}
	events, err := b.manual.Events(ctx, planID)
	if err != nil {
		b.answerCallback(ctx, cq.ID, "History lookup failed", true)
		b.log.Warn("telegram: list manual events failed", "plan", planID, "error", err.Error())
		return
	}
	b.answerCallback(ctx, cq.ID, "Opening history", false)
	b.sendManualReply(ctx, cq, reply{parts: formatManualHistoryAt(plan, events, b.location(ctx))})
}

// refreshManualMessage re-renders the plan after a successful transition. An
// edit failure is transient and never rolls back the persisted state.
func (b *Bot) refreshManualMessage(ctx context.Context, cq *callbackQuery, planID string) {
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
	b.editReply(ctx, cq, b.planReply(plan, occurrences))
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
		markup := rep.keyboard
		if len(rep.parts) > 1 {
			markup = nil
		}
		if err := b.api.editMessageText(ctx, cq.Message.Chat.ID, cq.Message.MessageID, rep.parts[0], markup); err == nil {
			if len(rep.parts) > 1 {
				if err := b.sendParts(ctx, cq.Message.Chat.ID, rep.parts[1:], rep.keyboard); err != nil {
					b.log.Warn("telegram: send remaining parts failed", "error", err.Error())
				}
			}
			return
		} else {
			b.log.Warn("telegram: editMessageText failed", "error", err.Error())
		}
	}
	b.sendManualReply(ctx, cq, rep)
}
