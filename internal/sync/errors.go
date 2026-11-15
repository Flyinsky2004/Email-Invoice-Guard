package syncengine

import (
	"context"
	"errors"
	"fmt"

	"email-invoice-guard/internal/core"
)

var (
	errProvider = errors.New("provider failure")
	errStorage  = errors.New("storage failure")
)

func storageError() error { return fmt.Errorf("%w", errStorage) }

// providerError converts an arbitrary provider error into a non-leaking
// sentinel while preserving context cancellation and authorization signals.
func providerError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch {
	case errors.Is(err, core.ErrReauthorization):
		return fmt.Errorf("%w: %w", errProvider, core.ErrReauthorization)
	case errors.Is(err, core.ErrUnauthorized):
		return fmt.Errorf("%w: %w", errProvider, core.ErrUnauthorized)
	default:
		return fmt.Errorf("%w", errProvider)
	}
}

// safeCode maps an error to a bounded diagnostic code that never contains
// provider payloads, credentials or message bodies.
func safeCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context_deadline"
	case errors.Is(err, core.ErrReauthorization):
		return "reauthorization_required"
	case errors.Is(err, core.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, core.ErrNotFound):
		return "not_found"
	case errors.Is(err, core.ErrInvalid):
		return "invalid"
	case errors.Is(err, errProvider):
		return "provider_error"
	case errors.Is(err, errStorage):
		return "storage_error"
	default:
		return "internal_error"
	}
}

func safeError(accountID string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("account %s: %s", accountID, safeCode(err))
}
