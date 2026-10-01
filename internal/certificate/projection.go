package certificate

import (
	"context"
	"sort"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

// transitRecord is the event-sourced evidence gathered for one transit cycle.
type transitRecord struct {
	chamberID   string
	cycle       model.TransitCycle
	plan        *model.ConvoyPlan
	transitions []StageTransition
	devices     map[uuid.UUID]*DeviceConfirmation
	incidents   []Incident
	firstSeq    uint64
	lastSeq     uint64
	issued      *Certificate
}

func newTransitRecord(chamberID string) *transitRecord {
	return &transitRecord{
		chamberID: chamberID,
		devices:   make(map[uuid.UUID]*DeviceConfirmation),
	}
}

func (r *transitRecord) deviceList() []DeviceConfirmation {
	result := make([]DeviceConfirmation, 0, len(r.devices))
	for _, device := range r.devices {
		result = append(result, *device)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].QueuedAt.Equal(result[j].QueuedAt) {
			return result[i].Sequence < result[j].Sequence
		}
		return result[i].QueuedAt.Before(result[j].QueuedAt)
	})
	return result
}

// projector folds one chamber event partition into per-cycle evidence.
type projector struct {
	store journal.Store
}

func (p *projector) chamber(ctx context.Context, chamberID string) (map[uuid.UUID]*transitRecord, error) {
	events, err := p.store.Events(ctx, chamberID, 0)
	if err != nil {
		return nil, err
	}
	records := make(map[uuid.UUID]*transitRecord)
	plans := make(map[uuid.UUID]model.ConvoyPlan)
	var latestCycle uuid.UUID

	attachPlan := func(plan model.ConvoyPlan) {
		plans[plan.ID] = plan
		for _, record := range records {
			if record.cycle.PlanID == plan.ID && record.plan == nil {
				planCopy := plan
				record.plan = &planCopy
			}
		}
	}
	recordForPlan := func(planID uuid.UUID) *transitRecord {
		for _, record := range records {
			if record.cycle.PlanID == planID {
				return record
			}
		}
		return nil
	}

	for _, event := range events {
		switch event.Type {
		case model.EventPlanCreated:
			// The recorder stamps the plan id in the event's CycleID field.
			var plan model.ConvoyPlan
			if err := event.Decode(&plan); err != nil {
				return nil, err
			}
			attachPlan(plan)
		case model.EventPlanCancelled:
			if target := recordForPlan(event.CycleID); target != nil {
				target.incidents = append(target.incidents, Incident{
					Severity: "notice", Summary: "Convoy plan cancelled and compensated",
					EventType: event.Type, Sequence: event.Sequence, OccurredAt: event.OccurredAt,
				})
			}
		case model.EventCycleCreated:
			var cycle model.TransitCycle
			if err := event.Decode(&cycle); err != nil {
				return nil, err
			}
			record := newTransitRecord(chamberID)
			record.cycle = cycle
			record.firstSeq = event.Sequence
			record.lastSeq = event.Sequence
			if plan, ok := plans[cycle.PlanID]; ok {
				planCopy := plan
				record.plan = &planCopy
			}
			records[cycle.ID] = record
			latestCycle = cycle.ID
		case model.EventCycleAdvanced:
			var cycle model.TransitCycle
			if err := event.Decode(&cycle); err != nil {
				return nil, err
			}
			record := records[event.CycleID]
			if record == nil {
				continue
			}
			record.cycle = cycle
			record.lastSeq = event.Sequence
			record.transitions = append(record.transitions, StageTransition{
				Stage: cycle.Stage, Reason: cycle.Reason,
				Sequence: event.Sequence, OccurredAt: event.OccurredAt,
			})
		case model.EventCommandQueued, model.EventCommandSettled:
			record := records[event.CycleID]
			if record == nil {
				continue
			}
			var command model.DeviceCommand
			if err := event.Decode(&command); err != nil {
				return nil, err
			}
			device := record.devices[command.ID]
			if device == nil {
				device = &DeviceConfirmation{
					Device: command.Device, Action: command.Action, Target: command.Target,
					State: command.State, QueuedAt: event.OccurredAt, Sequence: event.Sequence,
				}
				record.devices[command.ID] = device
			}
			if event.Type == model.EventCommandSettled {
				device.State = command.State
				settledAt := event.OccurredAt
				device.SettledAt = &settledAt
			}
			record.lastSeq = event.Sequence
		case model.EventEmergencyStarted, model.EventEmergencySettled:
			// Emergency events are not tied to a cycle id; charge them to the
			// most recently opened transit in this chamber.
			record := records[latestCycle]
			if record == nil {
				continue
			}
			severity, summary := "notice", "Emergency equipment reached stable state"
			if event.Type == model.EventEmergencyStarted {
				severity, summary = "critical", "Emergency stop requested"
			}
			var note string
			var emergency model.EmergencyRecord
			if err := event.Decode(&emergency); err == nil {
				note = emergency.Message
			}
			record.incidents = append(record.incidents, Incident{
				Severity: severity, Summary: summary, EventType: event.Type,
				Sequence: event.Sequence, OccurredAt: event.OccurredAt, Note: note,
			})
			record.lastSeq = event.Sequence
		case model.EventReplayWarning:
			if target := records[latestCycle]; target != nil {
				target.incidents = append(target.incidents, Incident{
					Severity: "warning", Summary: "Recovery sequence requires review",
					EventType: event.Type, Sequence: event.Sequence, OccurredAt: event.OccurredAt,
				})
			}
		case model.EventCertificateIssued:
			var cert Certificate
			if err := event.Decode(&cert); err != nil {
				return nil, err
			}
			if record := records[event.CycleID]; record != nil {
				certCopy := cert
				record.issued = &certCopy
				record.lastSeq = event.Sequence
			}
		}
	}
	return records, nil
}

func (p *projector) allChambers(ctx context.Context) (map[string]map[uuid.UUID]*transitRecord, error) {
	chambers, err := p.store.Chambers(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]map[uuid.UUID]*transitRecord, len(chambers))
	for _, chamberID := range chambers {
		records, err := p.chamber(ctx, chamberID)
		if err != nil {
			return nil, err
		}
		result[chamberID] = records
	}
	return result, nil
}
