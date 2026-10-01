package certificate_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/certificate"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

type fakeClock struct{ t time.Time }

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time { return c.t }
func (c *fakeClock) Add(d time.Duration) time.Time {
	c.t = c.t.Add(d)
	return c.t
}

type harness struct {
	ctx      context.Context
	store    journal.Store
	recorder *journal.Recorder
	issuer   *certificate.Issuer
	clock    *fakeClock
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clock := newFakeClock()
	store := journal.NewMemoryStore()
	recorder := journal.NewRecorder(store, clock.Now)
	return &harness{
		ctx: context.Background(), store: store, recorder: recorder,
		issuer: certificate.NewIssuer(store, recorder, clock.Now), clock: clock,
	}
}

func (h *harness) createPlan(chamberID string, direction model.Direction) model.ConvoyPlan {
	vessel, err := model.NewVessel("River Star", "RS-204", 86)
	if err != nil {
		panic(err)
	}
	plan, err := model.NewConvoyPlan(chamberID, direction, []model.Vessel{vessel}, h.clock.Now())
	if err != nil {
		panic(err)
	}
	if _, err := h.recorder.Record(h.ctx, chamberID, model.EventPlanCreated, plan.ID, 0, plan); err != nil {
		panic(err)
	}
	h.clock.Add(time.Minute)
	return plan
}

func (h *harness) createCycle(plan model.ConvoyPlan) model.TransitCycle {
	cycleValue, err := model.NewTransitCycle(plan.ChamberID, plan.ID, plan.Direction, 1, h.clock.Now())
	if err != nil {
		panic(err)
	}
	if _, err := h.recorder.Record(h.ctx, plan.ChamberID, model.EventCycleCreated, cycleValue.ID, 1, cycleValue); err != nil {
		panic(err)
	}
	h.clock.Add(30 * time.Second)
	return cycleValue
}

// advance records a cycle.advanced event in the same shape as the coordinator.
func (h *harness) advance(cycleValue *model.TransitCycle, stage model.TransitStage, reason string) {
	if err := cycleValue.Advance(stage, reason, h.clock.Now()); err != nil {
		panic(err)
	}
	if _, err := h.recorder.Record(h.ctx, cycleValue.ChamberID, model.EventCycleAdvanced, cycleValue.ID, cycleValue.Generation, *cycleValue); err != nil {
		panic(err)
	}
	h.clock.Add(2 * time.Minute)
}

func (h *harness) settleCommand(cycleValue model.TransitCycle, epoch uint64, device model.DeviceKind, action string) {
	command, err := model.NewDeviceCommand(cycleValue, epoch, device, action, 0, h.clock.Now())
	if err != nil {
		panic(err)
	}
	if _, err := h.recorder.Record(h.ctx, cycleValue.ChamberID, model.EventCommandQueued, cycleValue.ID, cycleValue.Generation, command); err != nil {
		panic(err)
	}
	h.clock.Add(45 * time.Second)
	if err := command.SetState(model.CommandSettled, h.clock.Now()); err != nil {
		panic(err)
	}
	if _, err := h.recorder.Record(h.ctx, cycleValue.ChamberID, model.EventCommandSettled, cycleValue.ID, cycleValue.Generation, command); err != nil {
		panic(err)
	}
}

// driveToCompletion replays the full happy path ending in a signal handoff.
func (h *harness) driveToCompletion(plan model.ConvoyPlan, cycleValue *model.TransitCycle) {
	h.advance(cycleValue, model.StageAdmitting, "matching gate command settled")
	h.settleCommand(*cycleValue, 1, model.DeviceUpstreamGate, "open")
	h.advance(cycleValue, model.StageSealed, "matching gate command settled")
	h.settleCommand(*cycleValue, 2, model.DeviceUpstreamGate, "close")
	h.advance(cycleValue, model.StageLeveling, "matching gate command settled")
	h.settleCommand(*cycleValue, 3, model.DeviceFillValve, "set_opening")
	h.advance(cycleValue, model.StageReleasing, "matching gate command settled")
	h.settleCommand(*cycleValue, 4, model.DeviceDownstreamGate, "open")
	h.advance(cycleValue, model.StageCompleted, "field signal handoff confirmed")
}

func TestIssueCompletedTransitAndIdempotent(t *testing.T) {
	h := newHarness(t)
	plan := h.createPlan("chamber-1", model.DirectionUpstream)
	cycleValue := h.createCycle(plan)
	h.driveToCompletion(plan, &cycleValue)

	cert, already, err := h.issuer.Issue(h.ctx, cycleValue.ID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if already {
		t.Fatal("first issuance must not report as duplicate")
	}
	if cert.Number == "" || !strings.HasPrefix(cert.Number, "LFC-2026-C1-") {
		t.Fatalf("unexpected certificate number %q", cert.Number)
	}
	if cert.CycleID != cycleValue.ID || cert.PlanID != plan.ID {
		t.Fatal("certificate does not reference the transit and plan")
	}
	if cert.Direction != model.DirectionUpstream || len(cert.Vessels) != 1 ||
		cert.Vessels[0].CallSign != "RS-204" {
		t.Fatal("certificate vessel evidence is wrong")
	}
	if cert.AdmittedAt.IsZero() || cert.ReleasedAt.IsZero() || cert.CompletedAt.IsZero() {
		t.Fatal("certificate timeline must carry admission, release and completion times")
	}
	if !cert.CompletedAt.Equal(cert.SignalHandoffAt) {
		t.Fatal("signal handoff time should equal completion time")
	}
	if len(cert.Devices) != 4 {
		t.Fatalf("expected 4 settled devices, got %d", len(cert.Devices))
	}
	for _, device := range cert.Devices {
		if device.State != model.CommandSettled || device.SettledAt == nil {
			t.Fatalf("device %s not confirmed settled", device.Device)
		}
	}
	if cert.ContentHash == "" || len(cert.ContentHash) != 64 {
		t.Fatal("certificate must carry a SHA-256 content hash")
	}

	// Re-issuing is idempotent: same number and hash, no new evidence.
	again, already, err := h.issuer.Issue(h.ctx, cycleValue.ID)
	if err != nil {
		t.Fatalf("second issue: %v", err)
	}
	if !already {
		t.Fatal("second issuance must report as duplicate")
	}
	if again.Number != cert.Number || again.ContentHash != cert.ContentHash {
		t.Fatal("idempotent re-issue changed the certificate")
	}

	// The issued certificate survives a fresh issuer over the same journal.
	fresh := certificate.NewIssuer(h.store, h.recorder, h.clock.Now)
	loaded, err := fresh.Get(h.ctx, cycleValue.ID)
	if err != nil {
		t.Fatalf("get after reload: %v", err)
	}
	if loaded.Number != cert.Number {
		t.Fatal("certificate did not survive reconstruction from the journal")
	}
}

func TestCertificateNumbersDoNotRepeat(t *testing.T) {
	h := newHarness(t)
	var numbers []string
	for range 2 {
		plan := h.createPlan("chamber-1", model.DirectionUpstream)
		cycleValue := h.createCycle(plan)
		h.driveToCompletion(plan, &cycleValue)
		cert, _, err := h.issuer.Issue(h.ctx, cycleValue.ID)
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		numbers = append(numbers, cert.Number)
	}
	if numbers[0] == numbers[1] {
		t.Fatalf("certificate numbers repeated: %s", numbers[0])
	}
}

func TestRejectCancelledTransit(t *testing.T) {
	h := newHarness(t)
	plan := h.createPlan("chamber-1", model.DirectionDownstream)
	cycleValue := h.createCycle(plan)
	h.advance(&cycleValue, model.StageAdmitting, "matching gate command settled")
	h.advance(&cycleValue, model.StageCancelled, "operator cancellation")

	_, _, err := h.issuer.Issue(h.ctx, cycleValue.ID)
	var rejection *certificate.RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("expected RejectionError, got %v", err)
	}
	if rejection.Stage != model.StageCancelled ||
		!strings.Contains(rejection.Reason, "cancelled") ||
		!strings.Contains(rejection.Reason, "operator cancellation") {
		t.Fatalf("cancellation reason not surfaced: %+v", rejection)
	}
}

func TestRejectEmergencyTransit(t *testing.T) {
	h := newHarness(t)
	plan := h.createPlan("chamber-2", model.DirectionUpstream)
	cycleValue := h.createCycle(plan)
	h.advance(&cycleValue, model.StageAdmitting, "matching gate command settled")
	h.advance(&cycleValue, model.StageSealed, "matching gate command settled")
	h.advance(&cycleValue, model.StageEmergency, "emergency")

	_, _, err := h.issuer.Issue(h.ctx, cycleValue.ID)
	var rejection *certificate.RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("expected RejectionError, got %v", err)
	}
	if rejection.Stage != model.StageEmergency || !strings.Contains(rejection.Reason, "emergency") {
		t.Fatalf("emergency reason not surfaced: %+v", rejection)
	}
}

func TestRejectIncompleteTransit(t *testing.T) {
	h := newHarness(t)
	plan := h.createPlan("chamber-1", model.DirectionUpstream)
	cycleValue := h.createCycle(plan)
	h.advance(&cycleValue, model.StageAdmitting, "matching gate command settled")

	_, _, err := h.issuer.Issue(h.ctx, cycleValue.ID)
	var rejection *certificate.RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("expected RejectionError, got %v", err)
	}
	if !strings.Contains(rejection.Reason, "not complete") {
		t.Fatalf("incomplete reason not surfaced: %q", rejection.Reason)
	}
}

func TestRejectCompletionWithoutSignalHandoff(t *testing.T) {
	h := newHarness(t)
	plan := h.createPlan("chamber-1", model.DirectionUpstream)
	cycleValue := h.createCycle(plan)
	h.advance(&cycleValue, model.StageAdmitting, "matching gate command settled")
	h.advance(&cycleValue, model.StageSealed, "matching gate command settled")
	h.advance(&cycleValue, model.StageLeveling, "matching gate command settled")
	h.advance(&cycleValue, model.StageReleasing, "matching gate command settled")
	h.advance(&cycleValue, model.StageCompleted, "manual override without field handoff")

	_, _, err := h.issuer.Issue(h.ctx, cycleValue.ID)
	var rejection *certificate.RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("expected RejectionError, got %v", err)
	}
	if !strings.Contains(rejection.Reason, "signal handoff") {
		t.Fatalf("handoff reason not surfaced: %q", rejection.Reason)
	}
}

func TestUnknownTransitNotFound(t *testing.T) {
	h := newHarness(t)
	if _, _, err := h.issuer.Issue(h.ctx, uuid.New()); !errors.Is(err, certificate.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestIncidentsCarriedIntoCertificate(t *testing.T) {
	h := newHarness(t)
	plan := h.createPlan("chamber-1", model.DirectionUpstream)
	cycleValue := h.createCycle(plan)
	// An emergency start against the chamber is exceptional evidence that must
	// still appear even when the transit later completes normally.
	if _, err := h.recorder.Record(h.ctx, plan.ChamberID, model.EventEmergencyStarted, uuid.Nil, 0, model.EmergencyRecord{
		ID: uuid.New(), ChamberID: plan.ChamberID, Status: model.EmergencyDraining,
		RequestedAt: h.clock.Now(), Message: "cancelling active actuator commands",
	}); err != nil {
		t.Fatal(err)
	}
	h.driveToCompletion(plan, &cycleValue)

	cert, _, err := h.issuer.Issue(h.ctx, cycleValue.ID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	found := false
	for _, incident := range cert.Incidents {
		if incident.EventType == model.EventEmergencyStarted && incident.Severity == "critical" {
			found = true
		}
	}
	if !found {
		t.Fatalf("emergency incident missing from certificate: %+v", cert.Incidents)
	}

	document, err := certificate.Document(cert)
	if err != nil {
		t.Fatalf("render document: %v", err)
	}
	html := string(document)
	if !strings.Contains(html, cert.Number) || !strings.Contains(html, "River Star") {
		t.Fatal("document missing certificate number or vessel")
	}
	if !strings.HasPrefix(certificate.DownloadName(cert), "lfc-2026-") {
		t.Fatalf("unexpected download name %q", certificate.DownloadName(cert))
	}
}

func TestListReportsEligibilityAndIssuedState(t *testing.T) {
	h := newHarness(t)

	completed := h.createPlan("chamber-1", model.DirectionUpstream)
	completedCycle := h.createCycle(completed)
	h.driveToCompletion(completed, &completedCycle)
	if _, _, err := h.issuer.Issue(h.ctx, completedCycle.ID); err != nil {
		t.Fatalf("issue: %v", err)
	}

	open := h.createPlan("chamber-2", model.DirectionDownstream)
	openCycle := h.createCycle(open)
	h.advance(&openCycle, model.StageAdmitting, "matching gate command settled")

	entries, err := h.issuer.List(h.ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	byCycle := map[uuid.UUID]certificate.Entry{}
	for _, entry := range entries {
		byCycle[entry.CycleID] = entry
	}
	if byCycle[completedCycle.ID].Certificate == nil ||
		byCycle[completedCycle.ID].Certificate.Number == "" {
		t.Fatal("completed transit must show issued certificate")
	}
	if byCycle[openCycle.ID].Eligible || byCycle[openCycle.ID].Reason == "" {
		t.Fatal("open transit must show refusal reason")
	}
}
