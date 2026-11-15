package manual

import (
	"context"
	"sort"
	"sync"
	"time"

	"email-invoice-guard/internal/core"
)

// fakeRepo is an in-memory core.ManualReminderRepository. It records calls,
// enforces the documented false conditions of CreateOccurrence and
// CreateDispatch, and supports per-method error injection so the service can
// be tested without SQLite.
type fakeRepo struct {
	mu          sync.Mutex
	plans       map[string]core.ManualPlan
	occurrences map[string]core.ManualOccurrence
	dispatches  map[string]core.ManualDispatch
	deliveries  map[string]core.Delivery
	events      []core.ManualReminderEvent
	messages    []core.TelegramMessage

	errs  map[string]error
	calls []string

	transitions       int
	createOccurrences int
	createDispatches  int
	updatePlanEvents  []core.ManualReminderEvent
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		plans:       map[string]core.ManualPlan{},
		occurrences: map[string]core.ManualOccurrence{},
		dispatches:  map[string]core.ManualDispatch{},
		deliveries:  map[string]core.Delivery{},
		errs:        map[string]error{},
	}
}

func (f *fakeRepo) record(name string) { f.calls = append(f.calls, name) }

func (f *fakeRepo) fail(name string) error { return f.errs[name] }

func (f *fakeRepo) setErr(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[name] = err
}

func (f *fakeRepo) callCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if call == name {
			n++
		}
	}
	return n
}

func (f *fakeRepo) putPlan(plan core.ManualPlan) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans[plan.ID] = plan
}

func (f *fakeRepo) putOccurrence(occurrence core.ManualOccurrence) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.occurrences[occurrence.ID] = occurrence
}

func (f *fakeRepo) putMessage(message core.TelegramMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, message)
}

func (f *fakeRepo) plan(id string) (core.ManualPlan, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	plan, ok := f.plans[id]
	return plan, ok
}

func (f *fakeRepo) occurrencesFor(planID string) []core.ManualOccurrence {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.ManualOccurrence
	for _, occurrence := range f.occurrences {
		if occurrence.PlanID == planID {
			out = append(out, occurrence)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DueDate < out[j].DueDate })
	return out
}

func (f *fakeRepo) dispatchesFor(planID string) []core.ManualDispatch {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.ManualDispatch
	for _, dispatch := range f.dispatches {
		if dispatch.PlanID == planID {
			out = append(out, dispatch)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LocalDate < out[j].LocalDate })
	return out
}

func (f *fakeRepo) deliveriesFor(planID string) []core.Delivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Delivery
	for _, delivery := range f.deliveries {
		if delivery.Payload.ManualPlanID == planID {
			out = append(out, delivery)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (f *fakeRepo) eventsFor(planID string) []core.ManualReminderEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.ManualReminderEvent
	for _, event := range f.events {
		if event.PlanID == planID {
			out = append(out, event)
		}
	}
	return out
}

func (f *fakeRepo) CreatePlan(ctx context.Context, plan core.ManualPlan, event core.ManualReminderEvent) (core.ManualPlan, error) {
	if err := ctx.Err(); err != nil {
		return core.ManualPlan{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CreatePlan")
	if err := f.fail("CreatePlan"); err != nil {
		return core.ManualPlan{}, err
	}
	if _, ok := f.plans[plan.ID]; ok {
		return core.ManualPlan{}, core.ErrConflict
	}
	f.plans[plan.ID] = plan
	f.events = append(f.events, event)
	return plan, nil
}

func (f *fakeRepo) UpdatePlan(ctx context.Context, plan core.ManualPlan, event core.ManualReminderEvent) (core.ManualPlan, error) {
	if err := ctx.Err(); err != nil {
		return core.ManualPlan{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("UpdatePlan")
	if err := f.fail("UpdatePlan"); err != nil {
		return core.ManualPlan{}, err
	}
	current, ok := f.plans[plan.ID]
	if !ok {
		return core.ManualPlan{}, core.ErrNotFound
	}
	if current.Status == core.ManualPlanCancelled {
		return core.ManualPlan{}, core.ErrConflict
	}
	f.plans[plan.ID] = plan
	f.events = append(f.events, event)
	f.updatePlanEvents = append(f.updatePlanEvents, event)
	return plan, nil
}

func (f *fakeRepo) GetPlan(ctx context.Context, id string) (core.ManualPlan, error) {
	if err := ctx.Err(); err != nil {
		return core.ManualPlan{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("GetPlan")
	if err := f.fail("GetPlan"); err != nil {
		return core.ManualPlan{}, err
	}
	plan, ok := f.plans[id]
	if !ok {
		return core.ManualPlan{}, core.ErrNotFound
	}
	return plan, nil
}

func (f *fakeRepo) ListPlans(ctx context.Context, filter core.ManualPlanFilter) ([]core.ManualPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListPlans")
	if err := f.fail("ListPlans"); err != nil {
		return nil, err
	}
	out := make([]core.ManualPlan, 0, len(f.plans))
	for _, plan := range f.plans {
		if filter.Status != "" && plan.Status != filter.Status {
			continue
		}
		out = append(out, plan)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (f *fakeRepo) PausePlan(ctx context.Context, id string, actorID int64, at time.Time) (core.ManualPlan, error) {
	if err := ctx.Err(); err != nil {
		return core.ManualPlan{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("PausePlan")
	if err := f.fail("PausePlan"); err != nil {
		return core.ManualPlan{}, err
	}
	plan, ok := f.plans[id]
	if !ok {
		return core.ManualPlan{}, core.ErrNotFound
	}
	if plan.Status != core.ManualPlanActive {
		return core.ManualPlan{}, core.ErrConflict
	}
	plan.Status = core.ManualPlanPaused
	plan.UpdatedAt = at.UTC()
	f.plans[id] = plan
	f.events = append(f.events, core.ManualReminderEvent{
		ID: core.NewID(), PlanID: id, EventType: core.ManualEventPlanPaused,
		ActorID: actorID, CreatedAt: at.UTC(), Metadata: "{}",
	})
	return plan, nil
}

func (f *fakeRepo) ResumePlan(ctx context.Context, id string, next core.Date, actorID int64, at time.Time) (core.ManualPlan, error) {
	if err := ctx.Err(); err != nil {
		return core.ManualPlan{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ResumePlan")
	if err := f.fail("ResumePlan"); err != nil {
		return core.ManualPlan{}, err
	}
	plan, ok := f.plans[id]
	if !ok {
		return core.ManualPlan{}, core.ErrNotFound
	}
	if plan.Status != core.ManualPlanPaused {
		return core.ManualPlan{}, core.ErrConflict
	}
	plan.Status = core.ManualPlanActive
	plan.NextDueDate = next
	plan.UpdatedAt = at.UTC()
	f.plans[id] = plan
	f.events = append(f.events, core.ManualReminderEvent{
		ID: core.NewID(), PlanID: id, EventType: core.ManualEventPlanResumed,
		ActorID: actorID, CreatedAt: at.UTC(), Metadata: "{}",
	})
	return plan, nil
}

func (f *fakeRepo) CancelPlan(ctx context.Context, id string, actorID int64, at time.Time) (core.ManualPlan, error) {
	if err := ctx.Err(); err != nil {
		return core.ManualPlan{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CancelPlan")
	if err := f.fail("CancelPlan"); err != nil {
		return core.ManualPlan{}, err
	}
	plan, ok := f.plans[id]
	if !ok {
		return core.ManualPlan{}, core.ErrNotFound
	}
	if plan.Status == core.ManualPlanCancelled {
		return core.ManualPlan{}, core.ErrConflict
	}
	cancelledAt := at.UTC()
	plan.Status = core.ManualPlanCancelled
	plan.CancelledAt = &cancelledAt
	plan.UpdatedAt = at.UTC()
	f.plans[id] = plan
	for occurrenceID, occurrence := range f.occurrences {
		if occurrence.PlanID == id && occurrence.Status == core.ManualOccurrencePending {
			occurrence.Status = core.ManualOccurrenceCancelled
			occurrence.UpdatedAt = at.UTC()
			f.occurrences[occurrenceID] = occurrence
		}
	}
	for dispatchID, dispatch := range f.dispatches {
		if dispatch.PlanID == id && dispatch.Status == core.DeliveryPending {
			dispatch.Status = core.DeliverySkipped
			f.dispatches[dispatchID] = dispatch
		}
	}
	for deliveryID, delivery := range f.deliveries {
		if delivery.Payload.ManualPlanID == id && delivery.Status == core.DeliveryPending {
			delivery.Status = core.DeliverySkipped
			f.deliveries[deliveryID] = delivery
		}
	}
	f.events = append(f.events, core.ManualReminderEvent{
		ID: core.NewID(), PlanID: id, EventType: core.ManualEventPlanCancelled,
		ActorID: actorID, CreatedAt: at.UTC(), Metadata: "{}",
	})
	return plan, nil
}

func (f *fakeRepo) CreateOccurrence(ctx context.Context, occurrence core.ManualOccurrence, next core.Date) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CreateOccurrence")
	if err := f.fail("CreateOccurrence"); err != nil {
		return false, err
	}
	plan, ok := f.plans[occurrence.PlanID]
	if !ok {
		return false, core.ErrNotFound
	}
	if plan.Status != core.ManualPlanActive {
		return false, nil
	}
	if plan.NextDueDate != occurrence.DueDate {
		return false, nil
	}
	for _, existing := range f.occurrences {
		if existing.PlanID == occurrence.PlanID && existing.DueDate == occurrence.DueDate {
			return false, nil
		}
	}
	f.occurrences[occurrence.ID] = occurrence
	plan.NextDueDate = next
	plan.UpdatedAt = occurrence.UpdatedAt
	f.plans[plan.ID] = plan
	f.events = append(f.events, core.ManualReminderEvent{
		ID: core.NewID(), PlanID: occurrence.PlanID, OccurrenceID: occurrence.ID,
		EventType: core.ManualEventOccurrenceCreated, ActorID: 0,
		CreatedAt: occurrence.CreatedAt, Metadata: "{}",
	})
	f.createOccurrences++
	return true, nil
}

func (f *fakeRepo) CreateDispatch(ctx context.Context, planID string, occurrenceIDs []string, dispatch core.ManualDispatch, delivery core.Delivery) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CreateDispatch")
	if err := f.fail("CreateDispatch"); err != nil {
		return false, err
	}
	if _, ok := f.plans[planID]; !ok {
		return false, core.ErrNotFound
	}
	pending := make([]string, 0, len(occurrenceIDs))
	for _, id := range occurrenceIDs {
		occurrence, ok := f.occurrences[id]
		if ok && occurrence.PlanID == planID && occurrence.Status == core.ManualOccurrencePending {
			pending = append(pending, id)
		}
	}
	if len(pending) == 0 {
		return false, nil
	}
	for _, existing := range f.dispatches {
		if existing.PlanID == planID && existing.LocalDate == dispatch.LocalDate && existing.Kind == dispatch.Kind {
			return false, nil
		}
	}
	for _, existing := range f.deliveries {
		if existing.DedupKey == delivery.DedupKey {
			return false, nil
		}
	}
	dispatch.OccurrenceIDs = pending
	f.dispatches[dispatch.ID] = dispatch
	f.deliveries[delivery.ID] = delivery
	f.createDispatches++
	return true, nil
}

func (f *fakeRepo) GetOccurrence(ctx context.Context, id string) (core.ManualOccurrence, error) {
	if err := ctx.Err(); err != nil {
		return core.ManualOccurrence{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("GetOccurrence")
	if err := f.fail("GetOccurrence"); err != nil {
		return core.ManualOccurrence{}, err
	}
	occurrence, ok := f.occurrences[id]
	if !ok {
		return core.ManualOccurrence{}, core.ErrNotFound
	}
	return occurrence, nil
}

func (f *fakeRepo) ListOccurrences(ctx context.Context, filter core.ManualOccurrenceFilter) ([]core.ManualOccurrence, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListOccurrences")
	if err := f.fail("ListOccurrences"); err != nil {
		return nil, err
	}
	out := make([]core.ManualOccurrence, 0, len(f.occurrences))
	for _, occurrence := range f.occurrences {
		if filter.PlanID != "" && occurrence.PlanID != filter.PlanID {
			continue
		}
		if filter.Status != "" && occurrence.Status != filter.Status {
			continue
		}
		if filter.DueOnOrBefore != "" && occurrence.DueDate > filter.DueOnOrBefore {
			continue
		}
		out = append(out, occurrence)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DueDate != out[j].DueDate {
			return out[i].DueDate < out[j].DueDate
		}
		return out[i].ID < out[j].ID
	})
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (f *fakeRepo) TransitionOccurrence(ctx context.Context, id string, from, to core.ManualOccurrenceStatus, actorID int64, at time.Time) (core.ManualOccurrence, error) {
	if err := ctx.Err(); err != nil {
		return core.ManualOccurrence{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("TransitionOccurrence")
	f.transitions++
	if err := f.fail("TransitionOccurrence"); err != nil {
		return core.ManualOccurrence{}, err
	}
	occurrence, ok := f.occurrences[id]
	if !ok {
		return core.ManualOccurrence{}, core.ErrNotFound
	}
	if occurrence.Status == to {
		return occurrence, nil
	}
	if occurrence.Status != from {
		return core.ManualOccurrence{}, core.ErrConflict
	}
	occurrence.Status = to
	occurrence.UpdatedAt = at.UTC()
	eventType := core.ManualEventPaymentConfirmationRevoked
	if to == core.ManualOccurrencePaid {
		confirmedAt := at.UTC()
		confirmedBy := actorID
		occurrence.ConfirmedAt = &confirmedAt
		occurrence.ConfirmedBy = &confirmedBy
		eventType = core.ManualEventPaymentConfirmed
	} else {
		occurrence.ConfirmedAt = nil
		occurrence.ConfirmedBy = nil
	}
	f.occurrences[id] = occurrence
	f.events = append(f.events, core.ManualReminderEvent{
		ID: core.NewID(), PlanID: occurrence.PlanID, OccurrenceID: occurrence.ID,
		EventType: eventType, ActorID: actorID, CreatedAt: at.UTC(), Metadata: "{}",
	})
	return occurrence, nil
}

func (f *fakeRepo) GetDispatch(ctx context.Context, id string) (core.ManualDispatch, error) {
	if err := ctx.Err(); err != nil {
		return core.ManualDispatch{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("GetDispatch")
	if err := f.fail("GetDispatch"); err != nil {
		return core.ManualDispatch{}, err
	}
	dispatch, ok := f.dispatches[id]
	if !ok {
		return core.ManualDispatch{}, core.ErrNotFound
	}
	return dispatch, nil
}

func (f *fakeRepo) ListPlanDispatches(ctx context.Context, planID string) ([]core.ManualDispatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListPlanDispatches")
	if err := f.fail("ListPlanDispatches"); err != nil {
		return nil, err
	}
	var out []core.ManualDispatch
	for _, dispatch := range f.dispatches {
		if dispatch.PlanID == planID {
			out = append(out, dispatch)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LocalDate != out[j].LocalDate {
			return out[i].LocalDate < out[j].LocalDate
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (f *fakeRepo) ListManualMessages(ctx context.Context, planID string) ([]core.TelegramMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListManualMessages")
	if err := f.fail("ListManualMessages"); err != nil {
		return nil, err
	}
	outbox := map[string]bool{}
	for _, dispatch := range f.dispatches {
		if dispatch.PlanID == planID {
			outbox[dispatch.OutboxID] = true
		}
	}
	var out []core.TelegramMessage
	for _, message := range f.messages {
		if outbox[message.OutboxID] {
			out = append(out, message)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Part != out[j].Part {
			return out[i].Part < out[j].Part
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (f *fakeRepo) ManualEvents(ctx context.Context, planID string) ([]core.ManualReminderEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ManualEvents")
	if err := f.fail("ManualEvents"); err != nil {
		return nil, err
	}
	var out []core.ManualReminderEvent
	for _, event := range f.events {
		if event.PlanID == planID {
			out = append(out, event)
		}
	}
	return out, nil
}

var _ core.ManualReminderRepository = (*fakeRepo)(nil)
