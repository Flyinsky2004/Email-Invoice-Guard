package manual

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

// Tick evaluates every ACTIVE plan at the supplied instant. It is
// deterministic for a given now: all calendar decisions use the plan's own
// timezone, every timestamp is derived from now, and duplicate work is
// rejected by the repository's persistent unique keys.
func (s *Service) Tick(ctx context.Context, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	plans, err := s.repo.ListPlans(ctx, core.ManualPlanFilter{Status: core.ManualPlanActive})
	if err != nil {
		return fmt.Errorf("list manual plans: %w", err)
	}
	var errs []error
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return err
		}
		if plan.Status != core.ManualPlanActive {
			continue
		}
		if err := s.tickPlan(ctx, plan, now); err != nil {
			errs = append(errs, fmt.Errorf("plan %s: %w", plan.ID, err))
		}
	}
	return errors.Join(errs...)
}

// Run ticks until ctx is cancelled. Invalid persisted configuration stops the
// loop; transient repository failures are logged and retried on the next tick.
func (s *Service) Run(ctx context.Context) error {
	if err := s.tick(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.tick(ctx); err != nil {
				return err
			}
		}
	}
}

func (s *Service) tick(ctx context.Context) error {
	err := s.Tick(ctx, s.now())
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil
	case errors.Is(err, core.ErrInvalid):
		return err
	default:
		s.logger.Warn("manual reminder tick failed", "error", err)
		return nil
	}
}

func (s *Service) tickPlan(ctx context.Context, plan core.ManualPlan, now time.Time) error {
	if strings.TrimSpace(plan.Timezone) == "" {
		return fmt.Errorf("%w: timezone is empty", core.ErrInvalid)
	}
	loc, err := time.LoadLocation(plan.Timezone)
	if err != nil {
		return fmt.Errorf("%w: timezone %q", core.ErrInvalid, plan.Timezone)
	}
	_, hour, minute, err := normalizeClock(plan.ReminderTime)
	if err != nil {
		return err
	}
	local := now.In(loc)
	if beforeClock(local, hour, minute) {
		return nil
	}
	today := core.LocalDate(now, loc)
	if err := s.materialize(ctx, plan, today, now); err != nil {
		return err
	}
	return s.dispatchSummary(ctx, plan, today, now)
}

// materialize creates the occurrences that are due on or before today. The
// catch-up window bounds how far back one tick may go: a plan whose next due
// date is older than MaxCatchUpMonths jumps to the first billing date on or
// after today instead of replaying unbounded history.
func (s *Service) materialize(ctx context.Context, plan core.ManualPlan, today core.Date, now time.Time) error {
	nextDue := plan.NextDueDate
	for nextDue != "" && nextDue <= today {
		behind, err := monthsBetween(nextDue, today)
		if err != nil {
			return err
		}
		if behind > s.maxCatchUp {
			target, err := firstBillingOnOrAfter(today, plan.BillingDay)
			if err != nil {
				return err
			}
			// The occurrence CAS advances from the stored next due date, so the
			// jump must be persisted before the current period can be
			// materialized. A crash between the two steps is safe: the next
			// tick sees next_due_date <= today and creates the occurrence.
			if err := s.persistJump(ctx, plan, target, now); err != nil {
				return err
			}
			if target <= today {
				return s.createOccurrence(ctx, plan, target, now)
			}
			return nil
		}
		next, err := nextBillingDate(nextDue, plan.BillingDay)
		if err != nil {
			return err
		}
		created, err := s.insertOccurrence(ctx, plan, nextDue, next, now)
		if err != nil {
			return err
		}
		if !created {
			// The occurrence already exists or the plan moved on; stop rather
			// than materializing a cascade against stale state.
			return nil
		}
		nextDue = next
	}
	return nil
}

func (s *Service) createOccurrence(ctx context.Context, plan core.ManualPlan, due core.Date, now time.Time) error {
	next, err := nextBillingDate(due, plan.BillingDay)
	if err != nil {
		return err
	}
	_, err = s.insertOccurrence(ctx, plan, due, next, now)
	return err
}

// persistJump advances next_due_date without creating a historical
// occurrence. The repository has no dedicated primitive for this, so the plan
// update is written with an audit event; a concurrent advance is respected by
// re-reading the plan first.
func (s *Service) persistJump(ctx context.Context, plan core.ManualPlan, target core.Date, now time.Time) error {
	current, err := s.repo.GetPlan(ctx, plan.ID)
	if err != nil {
		return fmt.Errorf("reload plan: %w", err)
	}
	if current.Status != core.ManualPlanActive || current.NextDueDate != plan.NextDueDate {
		return nil
	}
	current.NextDueDate = target
	current.UpdatedAt = now.UTC()
	event := core.ManualReminderEvent{
		ID:        core.NewID(),
		PlanID:    current.ID,
		EventType: core.ManualEventPlanUpdated,
		ActorID:   0,
		CreatedAt: now.UTC(),
		Metadata:  `{"reason":"catch_up"}`,
	}
	if _, err := s.repo.UpdatePlan(ctx, current, event); err != nil {
		return fmt.Errorf("persist catch-up jump: %w", err)
	}
	return nil
}

// dispatchSummary enqueues one DAILY_SUMMARY delivery per plan per plan-local
// day. It covers every pending occurrence due on or before today; the
// repository's UNIQUE(plan_id, local_date, kind) key is the final duplicate
// guard across repeated ticks and restarts.
func (s *Service) dispatchSummary(ctx context.Context, plan core.ManualPlan, today core.Date, now time.Time) error {
	occurrences, err := s.repo.ListOccurrences(ctx, core.ManualOccurrenceFilter{
		PlanID:        plan.ID,
		Status:        core.ManualOccurrencePending,
		DueOnOrBefore: today,
	})
	if err != nil {
		return fmt.Errorf("list pending occurrences: %w", err)
	}
	if len(occurrences) == 0 {
		return nil
	}
	// Re-read the plan immediately before enqueueing: a concurrent pause or
	// cancel must not produce a summary from the state observed earlier in the
	// tick.
	current, err := s.repo.GetPlan(ctx, plan.ID)
	if err != nil {
		return fmt.Errorf("reload plan: %w", err)
	}
	if current.Status != core.ManualPlanActive {
		return nil
	}
	ids := make([]string, 0, len(occurrences))
	for _, occurrence := range occurrences {
		ids = append(ids, occurrence.ID)
	}
	deliveryID := core.NewID()
	dispatch := core.ManualDispatch{
		ID:            core.NewID(),
		PlanID:        plan.ID,
		OutboxID:      deliveryID,
		LocalDate:     today,
		Kind:          core.ManualDispatchDailySummary,
		OccurrenceIDs: ids,
		Status:        core.DeliveryPending,
		CreatedAt:     now.UTC(),
	}
	delivery := core.Delivery{
		ID:       deliveryID,
		DedupKey: dedupKey(plan.ID, today),
		Payload: core.DeliveryPayload{
			Kind:                "manual",
			Text:                summaryText(plan, occurrences),
			ManualPlanID:        plan.ID,
			ManualOccurrenceIDs: ids,
		},
		Status:      core.DeliveryPending,
		AvailableAt: now.UTC(),
		CreatedAt:   now.UTC(),
	}
	created, err := s.repo.CreateDispatch(ctx, plan.ID, ids, dispatch, delivery)
	if err != nil {
		return fmt.Errorf("create dispatch: %w", err)
	}
	if !created {
		s.logger.Debug("manual summary already dispatched", "plan_id", plan.ID, "local_date", today)
	}
	return nil
}

func dedupKey(planID string, localDate core.Date) string {
	return "manual-reminder:" + planID + ":" + string(localDate)
}

// summaryText is a short plain-text fallback. The Telegram layer renders the
// full summary from the payload's plan and occurrence IDs.
func summaryText(plan core.ManualPlan, occurrences []core.ManualOccurrence) string {
	return fmt.Sprintf("Recurring bill %q: %d occurrence(s) due.", plan.Title, len(occurrences))
}
