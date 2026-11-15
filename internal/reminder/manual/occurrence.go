package manual

import (
	"context"
	"fmt"
	"time"

	"email-invoice-guard/internal/core"
)

// insertOccurrence creates one pending occurrence and CAS-advances the plan's
// next due date. The amount is snapshotted so later plan edits cannot rewrite
// financial history. A false result means the plan moved on or the occurrence
// already exists; the caller must not treat that as an error.
func (s *Service) insertOccurrence(ctx context.Context, plan core.ManualPlan, due, next core.Date, now time.Time) (bool, error) {
	occurrence := core.ManualOccurrence{
		ID:        core.NewID(),
		PlanID:    plan.ID,
		DueDate:   due,
		Amount:    cloneMoney(plan.Amount),
		Status:    core.ManualOccurrencePending,
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
	}
	created, err := s.repo.CreateOccurrence(ctx, occurrence, next)
	if err != nil {
		return false, fmt.Errorf("create occurrence: %w", err)
	}
	return created, nil
}

// ConfirmOccurrence moves one occurrence from PENDING_CONFIRMATION to
// PAID_CONFIRMED on behalf of an authorized user. The repository performs the
// compare-and-swap and writes the audit event atomically; replaying the same
// target is idempotent and adds no second event.
func (s *Service) ConfirmOccurrence(ctx context.Context, actorID int64, id string) (core.ManualOccurrence, error) {
	if err := s.authorize(actorID); err != nil {
		return core.ManualOccurrence{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.ManualOccurrence{}, err
	}
	occurrenceKey, err := occurrenceID(id)
	if err != nil {
		return core.ManualOccurrence{}, err
	}
	return s.repo.TransitionOccurrence(ctx, occurrenceKey,
		core.ManualOccurrencePending, core.ManualOccurrencePaid, actorID, s.now().UTC())
}

// RevokeOccurrence reverses a user confirmation. The reverse transition is
// authorized the same way and is idempotent on same-target replay.
func (s *Service) RevokeOccurrence(ctx context.Context, actorID int64, id string) (core.ManualOccurrence, error) {
	if err := s.authorize(actorID); err != nil {
		return core.ManualOccurrence{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.ManualOccurrence{}, err
	}
	occurrenceKey, err := occurrenceID(id)
	if err != nil {
		return core.ManualOccurrence{}, err
	}
	return s.repo.TransitionOccurrence(ctx, occurrenceKey,
		core.ManualOccurrencePaid, core.ManualOccurrencePending, actorID, s.now().UTC())
}
