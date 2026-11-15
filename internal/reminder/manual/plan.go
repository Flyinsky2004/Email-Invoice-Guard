package manual

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

const (
	monthEndLastDay = "last_day"
	defaultTime     = "09:00"
	defaultTimezone = "Asia/Taipei"
	defaultInterval = 30 * time.Second
	defaultCatchUp  = 24
)

// Service is the operator-facing manual reminder domain. It owns plan
// lifecycle, occurrence confirmation and the daily scheduler.
//
// Every mutating method authorizes the caller actorID against the configured
// Telegram allowlist before touching the repository, so an unauthorized call
// has no effect. Reads are unrestricted because the Telegram layer already
// gates chat and user access.
type Service struct {
	repo       core.ManualReminderRepository
	cfg        core.Config
	logger     *slog.Logger
	now        func() time.Time
	interval   time.Duration
	maxCatchUp int
}

// NewService returns a manual reminder service. Zero-valued scheduler settings
// fall back to the documented defaults so an incomplete Config cannot panic a
// running process; invalid plan input is reported per call as core.ErrInvalid.
func NewService(repo core.ManualReminderRepository, cfg core.Config, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	interval := cfg.Manual.SchedulerInterval
	if interval <= 0 {
		interval = defaultInterval
	}
	maxCatchUp := cfg.Manual.MaxCatchUpMonths
	if maxCatchUp <= 0 {
		maxCatchUp = defaultCatchUp
	}
	return &Service{
		repo:       repo,
		cfg:        cfg,
		logger:     logger,
		now:        time.Now,
		interval:   interval,
		maxCatchUp: maxCatchUp,
	}
}

func (s *Service) authorize(actorID int64) error {
	if !core.Authorized(actorID, s.cfg.Telegram.AllowedUserIDs) {
		return core.ErrUnauthorized
	}
	return nil
}

func (s *Service) defaultReminderTime() string {
	if v := strings.TrimSpace(s.cfg.Manual.DefaultTime); v != "" {
		return v
	}
	return defaultTime
}

func (s *Service) defaultPlanTimezone() string {
	if v := strings.TrimSpace(s.cfg.Manual.DefaultTimezone); v != "" {
		return v
	}
	return defaultTimezone
}

// planInput is a validated ManualReminderInput with normalized fields.
type planInput struct {
	title, providerName, assetID, description string
	amount                                    *core.Money
	billingDay                                int
	reminderTime                              string
	hour, minute                              int
	timezone                                  string
	loc                                       *time.Location
	paymentURL, notes                         string
}

// validate enforces every plan field rule from the v1 contract. Empty
// reminder time and timezone fall back to the configured defaults; an
// unsupported currency or month-end policy is rejected rather than stored.
func (s *Service) validate(in core.ManualReminderInput) (planInput, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return planInput{}, fmt.Errorf("%w: title is required", core.ErrInvalid)
	}
	if in.BillingDay < 1 || in.BillingDay > 31 {
		return planInput{}, fmt.Errorf("%w: billing day must be 1-31", core.ErrInvalid)
	}
	if policy := strings.TrimSpace(s.cfg.Manual.MonthEndPolicy); policy != "" && policy != monthEndLastDay {
		return planInput{}, fmt.Errorf("%w: month end policy %q", core.ErrInvalid, policy)
	}
	rawTime := strings.TrimSpace(in.ReminderTime)
	if rawTime == "" {
		rawTime = s.defaultReminderTime()
	}
	clock, hour, minute, err := normalizeClock(rawTime)
	if err != nil {
		return planInput{}, err
	}
	zone := strings.TrimSpace(in.Timezone)
	if zone == "" {
		zone = s.defaultPlanTimezone()
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return planInput{}, fmt.Errorf("%w: timezone %q", core.ErrInvalid, zone)
	}
	if in.Amount != nil {
		if _, ok := core.CurrencyDecimals(in.Amount.Currency); !ok {
			return planInput{}, fmt.Errorf("%w: currency %q", core.ErrInvalid, in.Amount.Currency)
		}
	}
	return planInput{
		title:        title,
		providerName: strings.TrimSpace(in.ProviderName),
		assetID:      strings.TrimSpace(in.AssetID),
		description:  strings.TrimSpace(in.Description),
		amount:       cloneMoney(in.Amount),
		billingDay:   in.BillingDay,
		reminderTime: clock,
		hour:         hour,
		minute:       minute,
		timezone:     zone,
		loc:          loc,
		paymentURL:   strings.TrimSpace(in.PaymentURL),
		notes:        strings.TrimSpace(in.Notes),
	}, nil
}

func cloneMoney(m *core.Money) *core.Money {
	if m == nil {
		return nil
	}
	v := *m
	return &v
}

func planID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", fmt.Errorf("%w: plan id is required", core.ErrInvalid)
	}
	return id, nil
}

func occurrenceID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", fmt.Errorf("%w: occurrence id is required", core.ErrInvalid)
	}
	return id, nil
}

// CreatePlan stores an ACTIVE plan whose first due date is the first billing
// date on or after the plan-local today. No past occurrence is created.
func (s *Service) CreatePlan(ctx context.Context, actorID int64, in core.ManualReminderInput) (core.ManualPlan, error) {
	if err := s.authorize(actorID); err != nil {
		return core.ManualPlan{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.ManualPlan{}, err
	}
	v, err := s.validate(in)
	if err != nil {
		return core.ManualPlan{}, err
	}
	now := s.now()
	today := core.LocalDate(now, v.loc)
	nextDue, err := firstBillingOnOrAfter(today, v.billingDay)
	if err != nil {
		return core.ManualPlan{}, err
	}
	plan := core.ManualPlan{
		ID:           core.NewID(),
		Title:        v.title,
		ProviderName: v.providerName,
		AssetID:      v.assetID,
		Description:  v.description,
		Amount:       v.amount,
		BillingDay:   v.billingDay,
		ReminderTime: v.reminderTime,
		Timezone:     v.timezone,
		PaymentURL:   v.paymentURL,
		Notes:        v.notes,
		Status:       core.ManualPlanActive,
		NextDueDate:  nextDue,
		CreatedAt:    now.UTC(),
		UpdatedAt:    now.UTC(),
	}
	event := core.ManualReminderEvent{
		ID:        core.NewID(),
		PlanID:    plan.ID,
		EventType: core.ManualEventPlanCreated,
		ActorID:   actorID,
		CreatedAt: now.UTC(),
		Metadata:  "{}",
	}
	return s.repo.CreatePlan(ctx, plan, event)
}

// UpdatePlan edits an existing plan. A cancelled plan is never resurrected and
// a cancelled plan's next due date is never silently re-armed. When an ACTIVE
// plan's billing day changes, the next due date is recomputed against the
// plan-local today; otherwise the stored schedule is preserved.
func (s *Service) UpdatePlan(ctx context.Context, actorID int64, id string, in core.ManualReminderInput) (core.ManualPlan, error) {
	if err := s.authorize(actorID); err != nil {
		return core.ManualPlan{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.ManualPlan{}, err
	}
	planKey, err := planID(id)
	if err != nil {
		return core.ManualPlan{}, err
	}
	v, err := s.validate(in)
	if err != nil {
		return core.ManualPlan{}, err
	}
	current, err := s.repo.GetPlan(ctx, planKey)
	if err != nil {
		return core.ManualPlan{}, err
	}
	if current.Status == core.ManualPlanCancelled {
		return core.ManualPlan{}, fmt.Errorf("%w: plan is cancelled", core.ErrConflict)
	}
	now := s.now()
	nextDue := current.NextDueDate
	if current.Status == core.ManualPlanActive && v.billingDay != current.BillingDay {
		today := core.LocalDate(now, v.loc)
		nextDue, err = firstBillingOnOrAfter(today, v.billingDay)
		if err != nil {
			return core.ManualPlan{}, err
		}
	}
	updated := current
	updated.Title = v.title
	updated.ProviderName = v.providerName
	updated.AssetID = v.assetID
	updated.Description = v.description
	updated.Amount = v.amount
	updated.BillingDay = v.billingDay
	updated.ReminderTime = v.reminderTime
	updated.Timezone = v.timezone
	updated.PaymentURL = v.paymentURL
	updated.Notes = v.notes
	updated.NextDueDate = nextDue
	updated.UpdatedAt = now.UTC()
	event := core.ManualReminderEvent{
		ID:        core.NewID(),
		PlanID:    updated.ID,
		EventType: core.ManualEventPlanUpdated,
		ActorID:   actorID,
		CreatedAt: now.UTC(),
		Metadata:  "{}",
	}
	return s.repo.UpdatePlan(ctx, updated, event)
}

// PausePlan stops new occurrences and summaries. Existing pending occurrences
// stay visible and confirmable.
func (s *Service) PausePlan(ctx context.Context, actorID int64, id string) (core.ManualPlan, error) {
	if err := s.authorize(actorID); err != nil {
		return core.ManualPlan{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.ManualPlan{}, err
	}
	planKey, err := planID(id)
	if err != nil {
		return core.ManualPlan{}, err
	}
	current, err := s.repo.GetPlan(ctx, planKey)
	if err != nil {
		return core.ManualPlan{}, err
	}
	if current.Status != core.ManualPlanActive {
		return core.ManualPlan{}, fmt.Errorf("%w: plan is not active", core.ErrConflict)
	}
	return s.repo.PausePlan(ctx, planKey, actorID, s.now().UTC())
}

// ResumePlan re-arms a paused plan at the first billing date on or after the
// plan-local today. Missed periods are never backfilled.
func (s *Service) ResumePlan(ctx context.Context, actorID int64, id string) (core.ManualPlan, error) {
	if err := s.authorize(actorID); err != nil {
		return core.ManualPlan{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.ManualPlan{}, err
	}
	planKey, err := planID(id)
	if err != nil {
		return core.ManualPlan{}, err
	}
	current, err := s.repo.GetPlan(ctx, planKey)
	if err != nil {
		return core.ManualPlan{}, err
	}
	if current.Status != core.ManualPlanPaused {
		return core.ManualPlan{}, fmt.Errorf("%w: plan is not paused", core.ErrConflict)
	}
	if strings.TrimSpace(current.Timezone) == "" {
		return core.ManualPlan{}, fmt.Errorf("%w: timezone is empty", core.ErrInvalid)
	}
	loc, err := time.LoadLocation(current.Timezone)
	if err != nil {
		return core.ManualPlan{}, fmt.Errorf("%w: timezone %q", core.ErrInvalid, current.Timezone)
	}
	now := s.now()
	today := core.LocalDate(now, loc)
	nextDue, err := firstBillingOnOrAfter(today, current.BillingDay)
	if err != nil {
		return core.ManualPlan{}, err
	}
	return s.repo.ResumePlan(ctx, planKey, nextDue, actorID, now.UTC())
}

// CancelPlan permanently stops scheduling and cancels pending occurrences and
// queued manual outbox rows in the repository transaction.
func (s *Service) CancelPlan(ctx context.Context, actorID int64, id string) (core.ManualPlan, error) {
	if err := s.authorize(actorID); err != nil {
		return core.ManualPlan{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.ManualPlan{}, err
	}
	planKey, err := planID(id)
	if err != nil {
		return core.ManualPlan{}, err
	}
	current, err := s.repo.GetPlan(ctx, planKey)
	if err != nil {
		return core.ManualPlan{}, err
	}
	if current.Status == core.ManualPlanCancelled {
		return core.ManualPlan{}, fmt.Errorf("%w: plan is already cancelled", core.ErrConflict)
	}
	return s.repo.CancelPlan(ctx, planKey, actorID, s.now().UTC())
}
