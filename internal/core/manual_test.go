package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestManualStatusAndEventStrings(t *testing.T) {
	cases := []struct{ got, want string }{
		{string(ManualPlanActive), "ACTIVE"},
		{string(ManualPlanPaused), "PAUSED"},
		{string(ManualPlanCancelled), "CANCELLED"},
		{string(ManualOccurrencePending), "PENDING_CONFIRMATION"},
		{string(ManualOccurrencePaid), "PAID_CONFIRMED"},
		{string(ManualOccurrenceCancelled), "CANCELLED"},
		{ManualDispatchDailySummary, "DAILY_SUMMARY"},
		{ManualEventPlanCreated, "PLAN_CREATED"},
		{ManualEventPlanUpdated, "PLAN_UPDATED"},
		{ManualEventPlanPaused, "PLAN_PAUSED"},
		{ManualEventPlanResumed, "PLAN_RESUMED"},
		{ManualEventPlanCancelled, "PLAN_CANCELLED"},
		{ManualEventOccurrenceCreated, "OCCURRENCE_CREATED"},
		{ManualEventReminderSent, "REMINDER_SENT"},
		{ManualEventPaymentConfirmed, "PAYMENT_CONFIRMED"},
		{ManualEventPaymentConfirmationRevoked, "PAYMENT_CONFIRMATION_REVOKED"},
		{ManualEventOccurrenceCancelled, "OCCURRENCE_CANCELLED"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Fatalf("got %q want %q", c.got, c.want)
		}
	}
}

func TestDeliveryPayloadManualFieldsRoundTrip(t *testing.T) {
	in := DeliveryPayload{
		Kind:                "manual",
		Text:                "summary",
		ManualPlanID:        "plan-1",
		ManualOccurrenceIDs: []string{"occ-1", "occ-2"},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out DeliveryPayload
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.ManualPlanID != in.ManualPlanID || len(out.ManualOccurrenceIDs) != 2 ||
		out.ManualOccurrenceIDs[0] != "occ-1" || out.ManualOccurrenceIDs[1] != "occ-2" {
		t.Fatalf("round trip %+v", out)
	}

	// A payload written before this contract must still decode and render.
	var legacy DeliveryPayload
	if err := json.Unmarshal([]byte(`{"Kind":"email","EmailID":"e-1","Text":"hi"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Kind != "email" || legacy.EmailID != "e-1" || legacy.ManualPlanID != "" || legacy.ManualOccurrenceIDs != nil {
		t.Fatalf("legacy payload %+v", legacy)
	}
	// Empty manual fields keep the existing delivery shape.
	raw, err = json.Marshal(DeliveryPayload{Kind: "invoice", InvoiceID: "i-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.ManualPlanID != "" || out.ManualOccurrenceIDs != nil {
		t.Fatalf("empty manual fields %+v", out)
	}
}

func TestManualOptionalFieldsMarshalAsNull(t *testing.T) {
	plan := ManualPlan{ID: "p-1", Status: ManualPlanActive, NextDueDate: "2026-12-01"}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Amount", "CancelledAt"} {
		if v, ok := fields[key]; !ok || v != nil {
			t.Fatalf("%s must marshal as null, got %#v", key, fields[key])
		}
	}

	occ := ManualOccurrence{ID: "o-1", PlanID: "p-1", Status: ManualOccurrencePending, DueDate: "2026-12-01"}
	raw, err = json.Marshal(occ)
	if err != nil {
		t.Fatal(err)
	}
	fields = map[string]any{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Amount", "NotifiedAt", "ConfirmedAt", "ConfirmedBy"} {
		if v, ok := fields[key]; !ok || v != nil {
			t.Fatalf("%s must marshal as null, got %#v", key, fields[key])
		}
	}

	// A populated optional amount survives the round trip.
	amount := Money{Minor: 1299, Currency: "USD"}
	raw, err = json.Marshal(ManualPlan{ID: "p-2", Amount: &amount})
	if err != nil {
		t.Fatal(err)
	}
	var decoded ManualPlan
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Amount == nil || *decoded.Amount != amount {
		t.Fatalf("amount %+v", decoded.Amount)
	}
}

// manualRepoFake proves ManualReminderRepository stays satisfiable without
// widening Repository or Store. It is a compile-time assertion only.
type manualRepoFake struct{}

func (manualRepoFake) CreatePlan(context.Context, ManualPlan, ManualReminderEvent) (ManualPlan, error) {
	return ManualPlan{}, nil
}
func (manualRepoFake) UpdatePlan(context.Context, ManualPlan, ManualReminderEvent) (ManualPlan, error) {
	return ManualPlan{}, nil
}
func (manualRepoFake) GetPlan(context.Context, string) (ManualPlan, error) { return ManualPlan{}, nil }
func (manualRepoFake) ListPlans(context.Context, ManualPlanFilter) ([]ManualPlan, error) {
	return nil, nil
}
func (manualRepoFake) PausePlan(context.Context, string, int64, time.Time) (ManualPlan, error) {
	return ManualPlan{}, nil
}
func (manualRepoFake) ResumePlan(context.Context, string, Date, int64, time.Time) (ManualPlan, error) {
	return ManualPlan{}, nil
}
func (manualRepoFake) CancelPlan(context.Context, string, int64, time.Time) (ManualPlan, error) {
	return ManualPlan{}, nil
}
func (manualRepoFake) CreateOccurrence(context.Context, ManualOccurrence, Date) (bool, error) {
	return false, nil
}
func (manualRepoFake) CreateDispatch(context.Context, string, []string, ManualDispatch, Delivery) (bool, error) {
	return false, nil
}
func (manualRepoFake) GetOccurrence(context.Context, string) (ManualOccurrence, error) {
	return ManualOccurrence{}, nil
}
func (manualRepoFake) ListOccurrences(context.Context, ManualOccurrenceFilter) ([]ManualOccurrence, error) {
	return nil, nil
}
func (manualRepoFake) TransitionOccurrence(context.Context, string, ManualOccurrenceStatus, ManualOccurrenceStatus, int64, time.Time) (ManualOccurrence, error) {
	return ManualOccurrence{}, nil
}
func (manualRepoFake) GetDispatch(context.Context, string) (ManualDispatch, error) {
	return ManualDispatch{}, nil
}
func (manualRepoFake) ListPlanDispatches(context.Context, string) ([]ManualDispatch, error) {
	return nil, nil
}
func (manualRepoFake) ListManualMessages(context.Context, string) ([]TelegramMessage, error) {
	return nil, nil
}
func (manualRepoFake) ManualEvents(context.Context, string) ([]ManualReminderEvent, error) {
	return nil, nil
}

var _ ManualReminderRepository = manualRepoFake{}

// manualServiceFake shadows the repository methods whose signatures differ and
// reuses the rest, so the service interface is proven independently.
type manualServiceFake struct{ manualRepoFake }

func (manualServiceFake) CreatePlan(context.Context, int64, ManualReminderInput) (ManualPlan, error) {
	return ManualPlan{}, nil
}
func (manualServiceFake) UpdatePlan(context.Context, int64, string, ManualReminderInput) (ManualPlan, error) {
	return ManualPlan{}, nil
}
func (manualServiceFake) PausePlan(context.Context, int64, string) (ManualPlan, error) {
	return ManualPlan{}, nil
}
func (manualServiceFake) ResumePlan(context.Context, int64, string) (ManualPlan, error) {
	return ManualPlan{}, nil
}
func (manualServiceFake) CancelPlan(context.Context, int64, string) (ManualPlan, error) {
	return ManualPlan{}, nil
}
func (manualServiceFake) ConfirmOccurrence(context.Context, int64, string) (ManualOccurrence, error) {
	return ManualOccurrence{}, nil
}
func (manualServiceFake) RevokeOccurrence(context.Context, int64, string) (ManualOccurrence, error) {
	return ManualOccurrence{}, nil
}
func (manualServiceFake) ListPlanMessages(context.Context, string) ([]TelegramMessage, error) {
	return nil, nil
}
func (manualServiceFake) Events(context.Context, string) ([]ManualReminderEvent, error) {
	return nil, nil
}

func (manualServiceFake) Tick(context.Context, time.Time) error { return nil }
func (manualServiceFake) Run(context.Context) error             { return nil }

var _ ManualReminderService = manualServiceFake{}
