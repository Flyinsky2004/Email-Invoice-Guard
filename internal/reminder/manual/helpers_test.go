package manual

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

const testActor int64 = 42

func testConfig(allowed ...int64) core.Config {
	return core.Config{
		Telegram: core.TelegramConfig{AllowedUserIDs: allowed},
		Manual: core.ManualReminderConfig{
			DefaultTime:       "09:00",
			DefaultTimezone:   "Asia/Taipei",
			MonthEndPolicy:    "last_day",
			SchedulerInterval: time.Second,
			MaxCatchUpMonths:  24,
		},
	}
}

func newTestService(repo *fakeRepo, allowed ...int64) *Service {
	return NewService(repo, testConfig(allowed...), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// fixedNow is 2026-11-15 09:00 in Asia/Taipei.
func fixedNow() time.Time {
	return time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC)
}

func taipeiTime(year int, month time.Month, day, hour, minute int) time.Time {
	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		panic(err)
	}
	return time.Date(year, month, day, hour, minute, 0, 0, loc).UTC()
}

func inputWithDay(day int) core.ManualReminderInput {
	return core.ManualReminderInput{
		Title:        "Hosting",
		BillingDay:   day,
		ReminderTime: "09:00",
		Timezone:     "Asia/Taipei",
	}
}

func mustCreatePlan(t *testing.T, s *Service, actor int64, in core.ManualReminderInput) core.ManualPlan {
	t.Helper()
	plan, err := s.CreatePlan(context.Background(), actor, in)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	return plan
}

// seedPlan inserts an ACTIVE plan directly, bypassing creation-time due-date
// computation so scheduling tests can start from an arbitrary schedule.
func seedPlan(repo *fakeRepo, id string, billingDay int, nextDue core.Date, timezone string) core.ManualPlan {
	now := fixedNow().UTC()
	plan := core.ManualPlan{
		ID:           id,
		Title:        "Hosting",
		BillingDay:   billingDay,
		ReminderTime: "09:00",
		Timezone:     timezone,
		Status:       core.ManualPlanActive,
		NextDueDate:  nextDue,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	repo.putPlan(plan)
	return plan
}

func eventTypes(events []core.ManualReminderEvent) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.EventType)
	}
	return out
}

func containsEvent(events []core.ManualReminderEvent, want string) bool {
	for _, event := range events {
		if event.EventType == want {
			return true
		}
	}
	return false
}
