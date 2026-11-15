// Package lifecycle implements the authorized invoice payment state machine.
//
// State changes happen only through explicit Confirm, Undo and Cancel calls.
// Payment status is never derived from email content: saved mail can at most
// create or update an invoice, never confirm or cancel it.
//
// Every call first verifies the actor whitelist, so unauthorized calls have no
// read or write effects. The repository performs the atomic status comparison,
// confirmation metadata write and event append; this package only supplies the
// expected source state, target state, actor and timestamp.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

// Service applies explicit, whitelisted payment state changes.
type Service struct {
	store   core.Store
	allowed []int64
	now     func() time.Time
	logger  *slog.Logger
}

// New returns a payment confirmation service restricted to the allowed actor IDs.
func New(store core.Store, allowed []int64) *Service {
	return &Service{
		store:   store,
		allowed: append([]int64(nil), allowed...),
		now:     time.Now,
		logger:  slog.Default(),
	}
}

// Confirm marks a pending invoice as paid by the given actor.
func (s *Service) Confirm(ctx context.Context, invoiceID string, actorID int64) (core.Invoice, error) {
	return s.transition(ctx, invoiceID, actorID, core.PendingConfirmation, core.PaidConfirmed)
}

// Undo returns a paid invoice to pending after an explicit user correction.
func (s *Service) Undo(ctx context.Context, invoiceID string, actorID int64) (core.Invoice, error) {
	return s.transition(ctx, invoiceID, actorID, core.PaidConfirmed, core.PendingConfirmation)
}

// Cancel cancels a pending or review-required invoice. A paid invoice cannot be
// silently cancelled; the caller must undo the payment first.
func (s *Service) Cancel(ctx context.Context, invoiceID string, actorID int64) (core.Invoice, error) {
	if err := s.authorize(ctx, actorID); err != nil {
		return core.Invoice{}, err
	}
	if err := validateInvoiceID(invoiceID); err != nil {
		return core.Invoice{}, err
	}
	invoice, err := s.store.GetInvoice(ctx, invoiceID)
	if err != nil {
		return core.Invoice{}, fmt.Errorf("load invoice: %w", err)
	}
	switch invoice.Status {
	case core.Cancelled:
		return invoice, nil
	case core.PendingConfirmation, core.ReviewRequired:
		return s.apply(ctx, invoiceID, actorID, invoice.Status, core.Cancelled)
	default:
		return core.Invoice{}, fmt.Errorf("%w: cannot cancel invoice in %s", core.ErrConflict, invoice.Status)
	}
}

func (s *Service) transition(ctx context.Context, invoiceID string, actorID int64, from, to core.InvoiceStatus) (core.Invoice, error) {
	if err := s.authorize(ctx, actorID); err != nil {
		return core.Invoice{}, err
	}
	if err := validateInvoiceID(invoiceID); err != nil {
		return core.Invoice{}, err
	}
	return s.apply(ctx, invoiceID, actorID, from, to)
}

func (s *Service) apply(ctx context.Context, invoiceID string, actorID int64, from, to core.InvoiceStatus) (core.Invoice, error) {
	at := s.now().UTC()
	invoice, err := s.store.TransitionInvoice(ctx, invoiceID, from, to, actorID, at)
	if err == nil {
		s.logger.Info("invoice transitioned",
			"invoice_id", invoiceID, "from", from, "to", to, "actor_user_id", actorID)
		return invoice, nil
	}
	// A concurrent call may have reached the same target state first. Treat that
	// as an idempotent replay instead of surfacing a spurious conflict.
	if errors.Is(err, core.ErrConflict) {
		if current, getErr := s.store.GetInvoice(ctx, invoiceID); getErr == nil && current.Status == to {
			return current, nil
		}
	}
	return core.Invoice{}, fmt.Errorf("transition invoice: %w", err)
}

func (s *Service) authorize(ctx context.Context, actorID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !core.Authorized(actorID, s.allowed) {
		s.logger.Warn("invoice transition rejected: unauthorized actor", "actor_user_id", actorID)
		return fmt.Errorf("%w: actor %d", core.ErrUnauthorized, actorID)
	}
	return nil
}

func validateInvoiceID(invoiceID string) error {
	if strings.TrimSpace(invoiceID) == "" {
		return fmt.Errorf("%w: invoice id", core.ErrInvalid)
	}
	return nil
}
