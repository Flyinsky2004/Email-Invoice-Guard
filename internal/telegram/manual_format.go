package telegram

import (
	"fmt"
	"time"

	"email-invoice-guard/internal/core"
)

// formatManualPlanAt renders one plan and its pending occurrences. Every part
// is independently valid escaped HTML within the Telegram size limit.
func formatManualPlanAt(plan core.ManualPlan, occurrences []core.ManualOccurrence) []string {
	c := newChunker(chunkBudget)
	c.writeHTML("<b>Manual reminder plan</b>")
	c.writeRaw(plan.Title)
	if plan.ProviderName != "" {
		c.writeRaw("Provider: " + plan.ProviderName)
	}
	c.writeRaw("Status: " + manualPlanStatusText(plan.Status))
	c.writeRaw("Amount: " + manualAmountText(plan.Amount))
	c.writeRaw(fmt.Sprintf("Billing day: %d", plan.BillingDay))
	c.writeRaw("Reminder: " + plan.ReminderTime + " (" + plan.Timezone + ")")
	c.writeRaw("Next due: " + string(plan.NextDueDate))
	if plan.Description != "" {
		c.writeRaw("Description: " + plan.Description)
	}
	if plan.Notes != "" {
		c.writeRaw("Notes: " + plan.Notes)
	}
	if trustedHTTPSURL(plan.PaymentURL) {
		c.writeRaw("Payment URL: " + plan.PaymentURL)
	}
	c.writeHTML("──────────")
	if len(occurrences) == 0 {
		c.writeRaw("Pending occurrences: none.")
	} else {
		c.writeHTML(fmt.Sprintf("<b>Pending occurrences (%d)</b>", len(occurrences)))
		for i, occ := range occurrences {
			c.writeRaw(fmt.Sprintf("%d. due %s — %s — %s (id=%s)",
				i+1, occ.DueDate, manualAmountText(occurrenceAmount(plan, occ)), manualOccurrenceStatusText(occ.Status), occ.ID))
		}
	}
	c.writeHTML("──────────")
	c.writeRaw("Payment status is manual: only an authorized user can mark an occurrence paid.")
	return c.finish()
}

// formatManualSummaryAt renders the daily summary delivered through the outbox.
// It covers every occurrence in the dispatch and never truncates content.
func formatManualSummaryAt(plan core.ManualPlan, occurrences []core.ManualOccurrence) []string {
	c := newChunker(chunkBudget)
	c.writeHTML("<b>Manual reminder</b>")
	c.writeRaw(plan.Title)
	if plan.ProviderName != "" {
		c.writeRaw("Provider: " + plan.ProviderName)
	}
	c.writeRaw("Plan status: " + manualPlanStatusText(plan.Status))
	for i, occ := range occurrences {
		c.writeHTML(fmt.Sprintf("<b>Occurrence %d</b>", i+1))
		c.writeRaw("Due: " + string(occ.DueDate))
		c.writeRaw("Amount: " + manualAmountText(occurrenceAmount(plan, occ)))
		c.writeRaw("Status: " + manualOccurrenceStatusText(occ.Status))
	}
	if trustedHTTPSURL(plan.PaymentURL) {
		c.writeRaw("Payment URL: " + plan.PaymentURL)
	}
	c.writeHTML("──────────")
	c.writeRaw("Payment status is manual: only an authorized user can mark an occurrence paid.")
	return c.finish()
}

// formatManualHistoryAt renders the plan event trail and dispatch history.
func formatManualHistoryAt(plan core.ManualPlan, events []core.ManualReminderEvent, dispatches []core.ManualDispatch, dispatchAvailable bool, loc *time.Location) []string {
	if loc == nil {
		loc = defaultDisplayLocation()
	}
	c := newChunker(chunkBudget)
	c.writeHTML("<b>Manual reminder history</b>")
	c.writeRaw(plan.Title)
	c.writeRaw("Status: " + manualPlanStatusText(plan.Status))
	c.writeHTML("──────────")
	if len(events) == 0 {
		c.writeRaw("Events: none.")
	} else {
		c.writeHTML(fmt.Sprintf("<b>Events (%d)</b>", len(events)))
		for _, event := range events {
			line := "• " + event.EventType + " at " + manualTimeText(event.CreatedAt, loc)
			if event.OccurrenceID != "" {
				line += " (occurrence " + event.OccurrenceID + ")"
			}
			c.writeRaw(line)
		}
	}
	c.writeHTML("──────────")
	switch {
	case !dispatchAvailable:
		c.writeRaw("Dispatches: history unavailable.")
	case len(dispatches) == 0:
		c.writeRaw("Dispatches: none.")
	default:
		c.writeHTML(fmt.Sprintf("<b>Dispatches (%d)</b>", len(dispatches)))
		for _, dispatch := range dispatches {
			c.writeRaw(fmt.Sprintf("• %s %s — %s (id=%s)", dispatch.LocalDate, dispatch.Kind, dispatch.Status, dispatch.ID))
		}
	}
	return c.finish()
}

// formatManualDispatchAt renders one dispatch detail for the mdc callback.
func formatManualDispatchAt(dispatch core.ManualDispatch, loc *time.Location) []string {
	if loc == nil {
		loc = defaultDisplayLocation()
	}
	c := newChunker(chunkBudget)
	c.writeHTML("<b>Manual dispatch</b>")
	c.writeRaw("Plan: " + dispatch.PlanID)
	c.writeRaw("Local date: " + string(dispatch.LocalDate))
	c.writeRaw("Kind: " + dispatch.Kind)
	c.writeRaw("Status: " + string(dispatch.Status))
	c.writeRaw(fmt.Sprintf("Occurrences: %d", len(dispatch.OccurrenceIDs)))
	if dispatch.SentAt != nil {
		c.writeRaw("Sent at: " + manualTimeText(*dispatch.SentAt, loc))
	}
	return c.finish()
}

// formatManualPreviewAt renders the wizard confirmation summary.
func formatManualPreviewAt(st *wizardState) []string {
	c := newChunker(chunkBudget)
	c.writeHTML("<b>Review manual reminder</b>")
	c.writeRaw("Title: " + st.Title)
	if st.ProviderName != "" {
		c.writeRaw("Provider: " + st.ProviderName)
	}
	if st.HasAmount {
		c.writeRaw("Amount: " + core.FormatMoney(core.Money{Minor: st.AmountMinor, Currency: st.Currency}))
	} else {
		c.writeRaw("Amount: not fixed")
	}
	c.writeRaw(fmt.Sprintf("Billing day: %d", st.BillingDay))
	c.writeRaw("Reminder: " + st.ReminderTime + " (" + st.Timezone + ")")
	if st.PaymentURL != "" {
		c.writeRaw("Payment URL: " + st.PaymentURL)
	} else {
		c.writeRaw("Payment URL: none")
	}
	if st.Notes != "" {
		c.writeRaw("Notes: " + st.Notes)
	}
	c.writeHTML("──────────")
	c.writeRaw("Confirm to save this plan.")
	return c.finish()
}

func occurrenceAmount(plan core.ManualPlan, occ core.ManualOccurrence) *core.Money {
	if occ.Amount != nil {
		return occ.Amount
	}
	return plan.Amount
}

func manualTimeText(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.In(loc).Format(displayTimeLayout)
}
