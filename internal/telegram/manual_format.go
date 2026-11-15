package telegram

import (
	"time"

	"email-invoice-guard/internal/core"
)

// formatManualPlanAt renders one plan and its pending occurrences. Every part
// is independently valid escaped HTML within the Telegram size limit.
func formatManualPlanAt(plan core.ManualPlan, occurrences []core.ManualOccurrence, tr translator) []string {
	c := newChunker(chunkBudget)
	c.writeHTML(tr.t("manualFormat.planTitle"))
	c.writeRaw(plan.Title)
	if plan.ProviderName != "" {
		c.writeRaw(tr.t("manualFormat.provider", plan.ProviderName))
	}
	c.writeRaw(tr.t("manualFormat.status", tr.planStatus(plan.Status)))
	c.writeRaw(tr.t("manualFormat.amount", manualAmountText(plan.Amount, tr)))
	c.writeRaw(tr.t("manualFormat.billingDay", plan.BillingDay))
	c.writeRaw(tr.t("manualFormat.reminder", plan.ReminderTime, plan.Timezone))
	c.writeRaw(tr.t("manualFormat.nextDue", string(plan.NextDueDate)))
	if plan.Description != "" {
		c.writeRaw(tr.t("manualFormat.description", plan.Description))
	}
	if plan.Notes != "" {
		c.writeRaw(tr.t("manualFormat.notes", plan.Notes))
	}
	if trustedHTTPSURL(plan.PaymentURL) {
		c.writeRaw(tr.t("manualFormat.paymentURL", plan.PaymentURL))
	}
	c.writeHTML("──────────")
	if len(occurrences) == 0 {
		c.writeRaw(tr.t("manualFormat.pendingNone"))
	} else {
		c.writeHTML(tr.t("manualFormat.pendingHeader", len(occurrences)))
		for i, occ := range occurrences {
			c.writeRaw(tr.t("manualFormat.occurrenceLine",
				i+1, occ.DueDate, manualAmountText(occurrenceAmount(plan, occ), tr), tr.occurrenceStatus(occ.Status), occ.ID))
		}
	}
	c.writeHTML("──────────")
	c.writeRaw(tr.t("manualFormat.manualNote"))
	return c.finish()
}

// formatManualSummaryAt renders the daily summary delivered through the outbox.
// It covers every occurrence in the dispatch and never truncates content.
func formatManualSummaryAt(plan core.ManualPlan, occurrences []core.ManualOccurrence, tr translator) []string {
	c := newChunker(chunkBudget)
	c.writeHTML(tr.t("manualFormat.summaryTitle"))
	c.writeRaw(plan.Title)
	if plan.ProviderName != "" {
		c.writeRaw(tr.t("manualFormat.provider", plan.ProviderName))
	}
	c.writeRaw(tr.t("manualFormat.planStatus", tr.planStatus(plan.Status)))
	for i, occ := range occurrences {
		c.writeHTML(tr.t("manualFormat.occurrenceHeader", i+1))
		c.writeRaw(tr.t("manualFormat.due", string(occ.DueDate)))
		c.writeRaw(tr.t("manualFormat.amount", manualAmountText(occurrenceAmount(plan, occ), tr)))
		c.writeRaw(tr.t("manualFormat.status", tr.occurrenceStatus(occ.Status)))
	}
	if trustedHTTPSURL(plan.PaymentURL) {
		c.writeRaw(tr.t("manualFormat.paymentURL", plan.PaymentURL))
	}
	c.writeHTML("──────────")
	c.writeRaw(tr.t("manualFormat.manualNote"))
	return c.finish()
}

// formatManualHistoryAt renders the plan event trail. Reminder and payment
// transitions are already recorded as events, so no separate dispatch log is
// read.
func formatManualHistoryAt(plan core.ManualPlan, events []core.ManualReminderEvent, loc *time.Location, tr translator) []string {
	if loc == nil {
		loc = defaultDisplayLocation()
	}
	c := newChunker(chunkBudget)
	c.writeHTML(tr.t("manualFormat.historyTitle"))
	c.writeRaw(plan.Title)
	c.writeRaw(tr.t("manualFormat.status", tr.planStatus(plan.Status)))
	c.writeHTML("──────────")
	if len(events) == 0 {
		c.writeRaw(tr.t("manualFormat.eventsNone"))
	} else {
		c.writeHTML(tr.t("manualFormat.eventsHeader", len(events)))
		for _, event := range events {
			line := tr.t("manualFormat.eventLine", event.EventType, manualTimeText(event.CreatedAt, loc, tr))
			if event.OccurrenceID != "" {
				line += tr.t("manualFormat.eventOccurrence", event.OccurrenceID)
			}
			c.writeRaw(line)
		}
	}
	return c.finish()
}

// formatManualPreviewAt renders the wizard confirmation summary.
func formatManualPreviewAt(st *wizardState, tr translator) []string {
	c := newChunker(chunkBudget)
	c.writeHTML(tr.t("manualFormat.previewTitle"))
	c.writeRaw(tr.t("manualFormat.title", st.Title))
	c.writeRaw(tr.t("manualFormat.period", firstNonEmpty(st.Period, "monthly")))
	if st.ProviderName != "" {
		c.writeRaw(tr.t("manualFormat.provider", st.ProviderName))
	}
	if st.HasAmount {
		c.writeRaw(tr.t("manualFormat.amount", core.FormatMoney(core.Money{Minor: st.AmountMinor, Currency: st.Currency})))
	} else {
		c.writeRaw(tr.t("manualFormat.amount", tr.t("manual.amountNotFixed")))
	}
	c.writeRaw(tr.t("manualFormat.billingDay", st.BillingDay))
	if st.AssetID != "" {
		c.writeRaw(tr.t("manualFormat.asset", st.AssetID))
	} else {
		c.writeRaw(tr.t("manualFormat.assetNone"))
	}
	c.writeRaw(tr.t("manualFormat.reminder", st.ReminderTime, st.Timezone))
	if st.PaymentURL != "" {
		c.writeRaw(tr.t("manualFormat.paymentURL", st.PaymentURL))
	} else {
		c.writeRaw(tr.t("manualFormat.paymentURLNone"))
	}
	if st.Notes != "" {
		c.writeRaw(tr.t("manualFormat.notes", st.Notes))
	}
	c.writeHTML("──────────")
	c.writeRaw(tr.t("manualFormat.confirmToSave"))
	return c.finish()
}

func occurrenceAmount(plan core.ManualPlan, occ core.ManualOccurrence) *core.Money {
	if occ.Amount != nil {
		return occ.Amount
	}
	return plan.Amount
}

func manualTimeText(t time.Time, loc *time.Location, tr translator) string {
	if t.IsZero() {
		return tr.t("common.unknown")
	}
	return t.In(loc).Format(displayTimeLayout)
}
