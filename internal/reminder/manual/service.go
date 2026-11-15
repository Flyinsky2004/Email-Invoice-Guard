// Package manual implements operator-declared recurring reminders: the
// calendar recurrence rules, plan and occurrence lifecycle, the daily
// scheduler and the user-facing service.
//
// A manual plan is independent of mail. Only an authorized user confirmation
// can mark an occurrence paid, and the scheduler never mutates payment state.
// All scheduling state is persisted through core.ManualReminderRepository, so
// a restart replays nothing and the repository's unique keys remain the final
// duplicate guard.
package manual

import (
	"context"

	"email-invoice-guard/internal/core"
)

var _ core.ManualReminderService = (*Service)(nil)

// GetPlan returns one plan.
func (s *Service) GetPlan(ctx context.Context, id string) (core.ManualPlan, error) {
	return s.repo.GetPlan(ctx, id)
}

// ListPlans returns plans matching the filter. A zero limit means unbounded.
func (s *Service) ListPlans(ctx context.Context, filter core.ManualPlanFilter) ([]core.ManualPlan, error) {
	return s.repo.ListPlans(ctx, filter)
}

// GetOccurrence returns one occurrence.
func (s *Service) GetOccurrence(ctx context.Context, id string) (core.ManualOccurrence, error) {
	return s.repo.GetOccurrence(ctx, id)
}

// ListOccurrences returns occurrences matching the filter. A zero limit means
// unbounded.
func (s *Service) ListOccurrences(ctx context.Context, filter core.ManualOccurrenceFilter) ([]core.ManualOccurrence, error) {
	return s.repo.ListOccurrences(ctx, filter)
}

// ListPlanMessages returns the Telegram messages delivered for a plan's
// dispatches.
func (s *Service) ListPlanMessages(ctx context.Context, planID string) ([]core.TelegramMessage, error) {
	return s.repo.ListManualMessages(ctx, planID)
}

// Events returns a plan's append-only audit events in insertion order.
func (s *Service) Events(ctx context.Context, planID string) ([]core.ManualReminderEvent, error) {
	return s.repo.ManualEvents(ctx, planID)
}
