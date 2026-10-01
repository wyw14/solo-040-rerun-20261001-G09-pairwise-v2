package certificate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

type fixture struct {
	store    *journal.MemoryStore
	recorder *journal.Recorder
	clock    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clock := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	store := journal.NewMemoryStore()
	f := &fixture{store: store, clock: clock}
	f.recorder = journal.NewRecorder(store, func() time.Time { return f.clock })
	return f
}

func (f *fixture) advance(minutes int) { f.clock = f.clock.Add(time.Duration(minutes) * time.Minute) }

func (f *fixture) record(t *testing.T, chamber string, kind model.EventType, cycleID uuid.UUID, gen uint64, payload any) {
	t.Helper()
	if _, err := f.recorder.Record(context.Background(), chamber, kind, cycleID, gen, payload); err != nil {
		t.Fatalf("record %s: %v", kind, err)
	}
}

func (f *fixture) plan(t *testing.T, chamber string, direction model.Direction) model.ConvoyPlan {
	t.Helper()
	vessel, err := model.NewVessel("River Star", "RS-204", 86)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := model.NewConvoyPlan(chamber, direction, []model.Vessel{vessel}, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.record(t, chamber, model.EventPlanCreated, plan.ID, 0, plan)
	return plan
}

// advanceCycle drives a cycle through every stage. When handoff is true the
// final transition is the confirmed signal handoff that completes a transit.
func (f *fixture) advanceCycle(t *testing.T, cycle model.TransitCycle, terminal model.TransitStage, reason string) model.TransitCycle {
	t.Helper()
	stages := []model.TransitStage{
		model.StageAdmitting, model.StageSealed, model.StageLeveling,
		model.StageReleasing, terminal,
	}
	current := cycle
	for _, stage := range stages {
		f.advance(1)
		if err := current.Advance(stage, reason, f.clock); err != nil {
			t.Fatalf("advance to %s: %v", stage, err)
		}
		f.record(t, current.ChamberID, model.EventCycleAdvanced, current.ID, current.Generation, current)
	}
	return current
}

func completedTransit(t *testing.T, f *fixture, chamber string, direction model.Direction) (model.ConvoyPlan, model.TransitCycle) {
	t.Helper()
	plan := f.plan(t, chamber, direction)
	f.advance(1)
	cycle, err := model.NewTransitCycle(chamber, plan.ID, direction, 1, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.record(t, chamber, model.EventCycleCreated, cycle.ID, cycle.Generation, cycle)

	// Admission publishes a proceed signal for the vessel: this is entry.
	f.advance(1)
	state := model.SignalState{
		VesselID: plan.Vessels[0].ID, CycleID: cycle.ID, Generation: cycle.Generation,
		Aspect: model.SignalProceed, UpdatedAt: f.clock,
	}
	f.record(t, chamber, model.EventSignalChanged, cycle.ID, cycle.Generation, state)

	// One gate command is issued and settled before the leveling transition.
	f.advance(1)
	queued, err := model.NewDeviceCommand(cycle, 1, model.DeviceUpstreamGate, "open", 0, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.record(t, chamber, model.EventCommandQueued, cycle.ID, cycle.Generation, queued)
	f.advance(1)
	if err := queued.SetState(model.CommandSettled, f.clock); err != nil {
		t.Fatal(err)
	}
	f.record(t, chamber, model.EventCommandSettled, cycle.ID, cycle.Generation, queued)

	cycle = f.advanceCycle(t, cycle, model.StageCompleted, "field signal handoff confirmed")
	return plan, cycle
}

func TestIssueCompletedTransit(t *testing.T) {
	f := newFixture(t)
	_, cycle := completedTransit(t, f, "chamber-1", model.DirectionUpstream)

	service := NewService(f.store)
	issuance, err := service.Issue(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issuance.Reject != nil {
		t.Fatalf("expected certificate, got rejection %s: %s", issuance.Reject.Code, issuance.Reject.Reason)
	}
	cert := issuance.Certificate
	if cert.Stage != model.StageCompleted {
		t.Fatalf("stage = %s", cert.Stage)
	}
	if cert.Number == "" || !strings.HasPrefix(cert.Number, "LFC-CHAMBER1-G1-") {
		t.Fatalf("unexpected certificate number %q", cert.Number)
	}
	if len(cert.Fingerprint) != 64 {
		t.Fatalf("fingerprint length = %d", len(cert.Fingerprint))
	}
	if len(cert.Vessels) != 1 || cert.Vessels[0].CallSign != "RS-204" {
		t.Fatalf("vessels not projected: %+v", cert.Vessels)
	}
	if cert.Direction != model.DirectionUpstream {
		t.Fatalf("direction = %s", cert.Direction)
	}
	if cert.AdmittedAt.IsZero() || !cert.ReleasedAt.Equal(cert.CompletedAt) {
		t.Fatalf("timeline not projected: admitted=%v released=%v completed=%v", cert.AdmittedAt, cert.ReleasedAt, cert.CompletedAt)
	}
	if len(cert.Devices) != 1 || cert.Devices[0].State != model.CommandSettled {
		t.Fatalf("device confirmation not projected: %+v", cert.Devices)
	}
	if len(cert.StageHistory) != 6 {
		t.Fatalf("stage history entries = %d, want 6", len(cert.StageHistory))
	}
	if len(cert.Anomalies) != 0 {
		t.Fatalf("expected no anomalies, got %+v", cert.Anomalies)
	}
	if cert.FirstSequence == 0 || cert.LastSequence < cert.FirstSequence {
		t.Fatalf("event range invalid: %d-%d", cert.FirstSequence, cert.LastSequence)
	}

	jsonBytes, err := RenderJSON(cert)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(jsonBytes), cert.Number) {
		t.Fatalf("json download missing certificate number:\n%s", jsonBytes)
	}
	htmlBytes, err := RenderHTML(cert)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(htmlBytes), cert.Number) || !strings.Contains(string(htmlBytes), "River Star") {
		t.Fatalf("html certificate missing key content")
	}
}

func TestIssuanceIsIdempotent(t *testing.T) {
	f := newFixture(t)
	_, cycle := completedTransit(t, f, "chamber-1", model.DirectionUpstream)

	service := NewService(f.store)
	first, err := service.Issue(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Issue again much later; wall-clock time must not change the document.
	f.advance(120)
	second, err := service.Get(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Certificate.Number != second.Certificate.Number {
		t.Fatalf("certificate number changed: %s vs %s", first.Certificate.Number, second.Certificate.Number)
	}
	if first.Certificate.Fingerprint != second.Certificate.Fingerprint {
		t.Fatalf("fingerprint changed: %s vs %s", first.Certificate.Fingerprint, second.Certificate.Fingerprint)
	}
	firstJSON, _ := RenderJSON(first.Certificate)
	secondJSON, _ := RenderJSON(second.Certificate)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("re-issued document bytes differ")
	}
}

func TestDistinctTransitsGetDistinctCertificates(t *testing.T) {
	f := newFixture(t)
	_, first := completedTransit(t, f, "chamber-1", model.DirectionUpstream)
	_, second := completedTransit(t, f, "chamber-2", model.DirectionDownstream)

	service := NewService(f.store)
	a, _ := service.Issue(context.Background(), first.ID)
	b, _ := service.Issue(context.Background(), second.ID)
	if a.Certificate.Number == b.Certificate.Number {
		t.Fatalf("distinct transits share certificate number %s", a.Certificate.Number)
	}
	if a.Certificate.Fingerprint == b.Certificate.Fingerprint {
		t.Fatalf("distinct transits share fingerprint")
	}
}

func TestRejectCancelledTransit(t *testing.T) {
	f := newFixture(t)
	plan := f.plan(t, "chamber-1", model.DirectionUpstream)
	f.advance(1)
	cycle, err := model.NewTransitCycle("chamber-1", plan.ID, model.DirectionUpstream, 1, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.record(t, "chamber-1", model.EventCycleCreated, cycle.ID, cycle.Generation, cycle)
	f.advance(1)
	if err := cycle.Advance(model.StageAdmitting, "matching gate command settled", f.clock); err != nil {
		t.Fatal(err)
	}
	f.record(t, "chamber-1", model.EventCycleAdvanced, cycle.ID, cycle.Generation, cycle)
	f.advance(1)
	if err := cycle.Advance(model.StageCancelled, "operator cancellation", f.clock); err != nil {
		t.Fatal(err)
	}
	f.record(t, "chamber-1", model.EventCycleAdvanced, cycle.ID, cycle.Generation, cycle)
	f.record(t, "chamber-1", model.EventPlanCancelled, plan.ID, 0, map[string]any{"note": "operator cancellation"})

	service := NewService(f.store)
	issuance, err := service.Issue(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issuance.Certificate != nil {
		t.Fatal("cancelled transit must not be certified")
	}
	if issuance.Reject == nil || issuance.Reject.Code != RejectCancelled {
		t.Fatalf("reject = %+v, want %s", issuance.Reject, RejectCancelled)
	}
}

// advanceTo moves a cycle forward through the ordered non-terminal stages.
func (f *fixture) advanceTo(t *testing.T, cycle *model.TransitCycle, target model.TransitStage, reason string) {
	t.Helper()
	for cycle.Stage != target {
		f.advance(1)
		if err := cycle.Advance(target, reason, f.clock); err == nil {
			f.record(t, cycle.ChamberID, model.EventCycleAdvanced, cycle.ID, cycle.Generation, *cycle)
			return
		}
		next, err := nextOrdered(cycle.Stage)
		if err != nil {
			t.Fatalf("cannot advance toward %s from %s: %v", target, cycle.Stage, err)
		}
		if err := cycle.Advance(next, "matching gate command settled", f.clock); err != nil {
			t.Fatalf("advance to %s: %v", next, err)
		}
		f.record(t, cycle.ChamberID, model.EventCycleAdvanced, cycle.ID, cycle.Generation, *cycle)
	}
}

func nextOrdered(stage model.TransitStage) (model.TransitStage, error) {
	switch stage {
	case model.StagePlanned:
		return model.StageAdmitting, nil
	case model.StageAdmitting:
		return model.StageSealed, nil
	case model.StageSealed:
		return model.StageLeveling, nil
	case model.StageLeveling:
		return model.StageReleasing, nil
	default:
		return "", errors.New("no following stage")
	}
}

func TestRejectEmergencyTransit(t *testing.T) {
	f := newFixture(t)
	plan := f.plan(t, "chamber-1", model.DirectionUpstream)
	f.advance(1)
	cycle, err := model.NewTransitCycle("chamber-1", plan.ID, model.DirectionUpstream, 1, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.record(t, "chamber-1", model.EventCycleCreated, cycle.ID, cycle.Generation, cycle)
	f.advanceTo(t, &cycle, model.StageLeveling, "")
	f.advance(1)
	if err := cycle.Advance(model.StageEmergency, "emergency", f.clock); err != nil {
		t.Fatal(err)
	}
	f.record(t, "chamber-1", model.EventCycleAdvanced, cycle.ID, cycle.Generation, cycle)
	f.record(t, "chamber-1", model.EventEmergencyStarted, uuid.Nil, 0, model.EmergencyRecord{
		ID: uuid.New(), ChamberID: "chamber-1", Status: model.EmergencyDraining,
		RequestedAt: f.clock, Message: "cancelling active actuator commands",
	})

	service := NewService(f.store)
	issuance, err := service.Issue(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issuance.Certificate != nil {
		t.Fatal("emergency transit must not be certified")
	}
	if issuance.Reject == nil || issuance.Reject.Code != RejectEmergency {
		t.Fatalf("reject = %+v, want %s", issuance.Reject, RejectEmergency)
	}
}

func TestRejectInProgressAndMissing(t *testing.T) {
	f := newFixture(t)
	plan := f.plan(t, "chamber-1", model.DirectionUpstream)
	cycle, err := model.NewTransitCycle("chamber-1", plan.ID, model.DirectionUpstream, 1, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.record(t, "chamber-1", model.EventCycleCreated, cycle.ID, cycle.Generation, cycle)

	service := NewService(f.store)
	issuance, err := service.Issue(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issuance.Reject == nil || issuance.Reject.Code != RejectInProgress {
		t.Fatalf("reject = %+v, want %s", issuance.Reject, RejectInProgress)
	}

	missing, err := service.Issue(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if missing.Reject == nil || missing.Reject.Code != RejectNotFound {
		t.Fatalf("reject = %+v, want %s", missing.Reject, RejectNotFound)
	}
}

func TestRejectCompletedWithoutHandoff(t *testing.T) {
	f := newFixture(t)
	plan := f.plan(t, "chamber-1", model.DirectionUpstream)
	cycle, err := model.NewTransitCycle("chamber-1", plan.ID, model.DirectionUpstream, 1, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.record(t, "chamber-1", model.EventCycleCreated, cycle.ID, cycle.Generation, cycle)
	f.advanceTo(t, &cycle, model.StageReleasing, "")
	// A completed terminal stage with a reason other than the handoff marker
	// must never be certified.
	f.advance(1)
	if err := cycle.Advance(model.StageCompleted, "manual override", f.clock); err != nil {
		t.Fatal(err)
	}
	f.record(t, "chamber-1", model.EventCycleAdvanced, cycle.ID, cycle.Generation, cycle)

	service := NewService(f.store)
	issuance, err := service.Issue(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issuance.Certificate != nil {
		t.Fatal("completed transit without handoff confirmation must not be certified")
	}
	if issuance.Reject == nil || issuance.Reject.Code != RejectSignalMissing {
		t.Fatalf("reject = %+v, want %s", issuance.Reject, RejectSignalMissing)
	}
}

func TestCompletedTransitCarriesAnomalies(t *testing.T) {
	f := newFixture(t)
	_, cycle := completedTransit(t, f, "chamber-1", model.DirectionUpstream)
	// A recovery warning recorded after completion is still attributed to the
	// transit and must appear on the certificate.
	f.record(t, "chamber-1", model.EventReplayWarning, cycle.ID, cycle.Generation, map[string]any{
		"warning": "review required",
	})

	service := NewService(f.store)
	issuance, err := service.Issue(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issuance.Certificate == nil {
		t.Fatalf("completed transit should certify despite anomaly: %+v", issuance.Reject)
	}
	found := false
	for _, anomaly := range issuance.Certificate.Anomalies {
		if anomaly.Type == model.EventReplayWarning {
			found = true
		}
	}
	if !found {
		t.Fatalf("replay warning missing from anomalies: %+v", issuance.Certificate.Anomalies)
	}
}

func TestListIncludesEligibilityState(t *testing.T) {
	f := newFixture(t)
	_, done := completedTransit(t, f, "chamber-1", model.DirectionUpstream)

	// A cancelled transit on the second chamber.
	plan := f.plan(t, "chamber-2", model.DirectionDownstream)
	cycle, err := model.NewTransitCycle("chamber-2", plan.ID, model.DirectionDownstream, 1, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.record(t, "chamber-2", model.EventCycleCreated, cycle.ID, cycle.Generation, cycle)
	if err := cycle.Advance(model.StageCancelled, "operator cancellation", f.clock); err != nil {
		t.Fatal(err)
	}
	f.record(t, "chamber-2", model.EventCycleAdvanced, cycle.ID, cycle.Generation, cycle)

	service := NewService(f.store)
	summaries, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries = %d, want 2", len(summaries))
	}
	byID := map[uuid.UUID]Summary{}
	for _, summary := range summaries {
		byID[summary.CycleID] = summary
	}
	if byID[done.ID].Certificate == nil {
		t.Fatal("completed transit should carry a certificate in listing")
	}
	cancelled := byID[cycle.ID]
	if cancelled.Reject == nil || cancelled.Reject.Code != RejectCancelled {
		t.Fatalf("cancelled summary = %+v", cancelled.Reject)
	}
}
