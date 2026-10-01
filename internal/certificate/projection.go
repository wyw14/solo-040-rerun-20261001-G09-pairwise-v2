package certificate

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

// handoffReason is the terminal reason stamped by cycle.applySignalTransition
// when a releasing transit is completed by a confirmed field signal handoff.
const handoffReason = "field signal handoff confirmed"

// signalCompensation is the payload recorded when a signal publication is
// rolled back during cancellation.
type signalCompensation struct {
	VesselID    uuid.UUID `json:"vessel_id"`
	Compensated bool      `json:"compensated"`
}

// transitProjection accumulates the evidence of one transit generation.
type transitProjection struct {
	chamberID  string
	cycleID    uuid.UUID
	planID     uuid.UUID
	generation uint64
	direction  model.Direction
	current    model.TransitCycle

	plan         *model.ConvoyPlan
	stages       []StageTime
	commands     map[uuid.UUID]*DeviceConfirmation
	commandOrder []uuid.UUID
	admission    []model.SignalState
	anomalies    []AnomalyEvent

	firstSequence uint64
	lastSequence  uint64
}

func newTransitProjection(chamberID string, cycle model.TransitCycle, sequence uint64) *transitProjection {
	return &transitProjection{
		chamberID: chamberID, cycleID: cycle.ID, planID: cycle.PlanID,
		generation: cycle.Generation, direction: cycle.Direction, current: cycle,
		commands:      make(map[uuid.UUID]*DeviceConfirmation),
		stages:        []StageTime{{Stage: cycle.Stage, OccurredAt: cycle.CreatedAt, Sequence: sequence}},
		firstSequence: sequence, lastSequence: sequence,
	}
}

func (p *transitProjection) touch(sequence uint64) {
	if p.firstSequence == 0 || sequence < p.firstSequence {
		p.firstSequence = sequence
	}
	if sequence > p.lastSequence {
		p.lastSequence = sequence
	}
}

// Builder projects certificates from durable journal state.
type Builder struct {
	store journal.Store
}

// NewBuilder creates a certificate builder over the event store.
func NewBuilder(store journal.Store) *Builder {
	return &Builder{store: store}
}

// loadChamber scans one chamber partition, grouping events by transit cycle.
// Plans are keyed separately because plan events carry the plan id where cycle
// events normally carry the cycle id.
func (b *Builder) loadChamber(ctx context.Context, chamberID string) (map[uuid.UUID]*transitProjection, map[uuid.UUID]model.ConvoyPlan, error) {
	events, err := b.store.Events(ctx, chamberID, 0)
	if err != nil {
		return nil, nil, err
	}
	cycles := make(map[uuid.UUID]*transitProjection)
	plans := make(map[uuid.UUID]model.ConvoyPlan)
	var latest *transitProjection
	for _, event := range events {
		// Events arrive in append order, which is what makes the "latest
		// touched cycle" attribution below deterministic.
		switch event.Type {
		case model.EventPlanCreated:
			var plan model.ConvoyPlan
			if err := event.Decode(&plan); err != nil {
				return nil, nil, err
			}
			plans[plan.ID] = plan
		case model.EventPlanCancelled:
			// The recorder stores the plan id in the event's CycleID field, so
			// the cancellation can be attributed to its exact transit instead
			// of whichever transit was touched last.
			note := ""
			var payload map[string]any
			if err := event.Decode(&payload); err == nil {
				if value, ok := payload["note"].(string); ok {
					note = value
				}
			}
			if target := cyclesByPlan(cycles, event.CycleID); target != nil {
				target.anomalies = append(target.anomalies, AnomalyEvent{
					Type: event.Type, Sequence: event.Sequence,
					OccurredAt: event.OccurredAt,
					Summary:    anomalySummary(event.Type, note),
				})
				target.touch(event.Sequence)
			}
		case model.EventCycleCreated:
			var cycle model.TransitCycle
			if err := event.Decode(&cycle); err != nil {
				return nil, nil, err
			}
			projection := newTransitProjection(chamberID, cycle, event.Sequence)
			cycles[cycle.ID] = projection
			latest = projection
		case model.EventCycleAdvanced:
			var cycle model.TransitCycle
			if err := event.Decode(&cycle); err != nil {
				return nil, nil, err
			}
			projection := cycles[cycle.ID]
			if projection == nil {
				projection = newTransitProjection(chamberID, cycle, event.Sequence)
				cycles[cycle.ID] = projection
			}
			projection.current = cycle
			projection.planID = cycle.PlanID
			projection.stages = append(projection.stages, StageTime{
				Stage: cycle.Stage, OccurredAt: event.OccurredAt, Sequence: event.Sequence,
			})
			projection.touch(event.Sequence)
			latest = projection
		case model.EventCommandQueued:
			var command model.DeviceCommand
			if err := event.Decode(&command); err != nil {
				return nil, nil, err
			}
			if projection := cycles[command.CycleID]; projection != nil {
				if _, exists := projection.commands[command.ID]; !exists {
					projection.commandOrder = append(projection.commandOrder, command.ID)
				}
				projection.commands[command.ID] = &DeviceConfirmation{
					CommandID: command.ID, Device: command.Device, Action: command.Action,
					Target: command.Target, State: command.State,
					IssuedAt: command.CreatedAt, Sequence: event.Sequence,
				}
				projection.touch(event.Sequence)
			}
		case model.EventCommandSettled:
			var command model.DeviceCommand
			if err := event.Decode(&command); err != nil {
				return nil, nil, err
			}
			if projection := cycles[command.CycleID]; projection != nil {
				confirmation := projection.commands[command.ID]
				if confirmation == nil {
					projection.commandOrder = append(projection.commandOrder, command.ID)
					confirmation = &DeviceConfirmation{
						CommandID: command.ID, Device: command.Device, Action: command.Action,
						Target: command.Target, IssuedAt: command.CreatedAt,
					}
					projection.commands[command.ID] = confirmation
				}
				confirmation.State = command.State
				confirmation.SettledAt = command.UpdatedAt
				if confirmation.IssuedAt.IsZero() {
					confirmation.IssuedAt = command.CreatedAt
				}
				projection.touch(event.Sequence)
			}
		case model.EventSignalChanged:
			b.applySignalEvent(event, cycles)
		case model.EventEmergencyStarted, model.EventEmergencySettled, model.EventReplayWarning:
			// Emergency events are only part of a transit when that transit was
			// itself terminated by the emergency. An emergency stop issued while
			// no transit is active (or after an older one completed) must not be
			// attached to that older, already-certifiable transit.
			target := latest
			if event.Type != model.EventReplayWarning &&
				(target == nil || target.current.Stage != model.StageEmergency) {
				target = nil
			}
			if target != nil {
				detail := ""
				var record model.EmergencyRecord
				if err := event.Decode(&record); err == nil && record.Message != "" {
					detail = record.Message
				}
				target.anomalies = append(target.anomalies, AnomalyEvent{
					Type: event.Type, Sequence: event.Sequence,
					OccurredAt: event.OccurredAt,
					Summary:    anomalySummary(event.Type, detail),
				})
				target.touch(event.Sequence)
			}
		}
	}
	return cycles, plans, nil
}

// cyclesByPlan returns the projection whose transit used the given plan id.
func cyclesByPlan(cycles map[uuid.UUID]*transitProjection, planID uuid.UUID) *transitProjection {
	if planID == uuid.Nil {
		return nil
	}
	for _, projection := range cycles {
		if projection.planID == planID {
			return projection
		}
	}
	return nil
}

func (b *Builder) applySignalEvent(event model.Event, cycles map[uuid.UUID]*transitProjection) {
	var state model.SignalState
	if err := event.Decode(&state); err == nil && state.VesselID != uuid.Nil && state.Aspect != "" {
		if projection := cycles[state.CycleID]; projection != nil {
			projection.admission = append(projection.admission, state)
			projection.touch(event.Sequence)
		}
		return
	}
	// A compensated rollback carries no aspect; record it as an anomaly on
	// the transit that owned the publication.
	var compensation signalCompensation
	if err := event.Decode(&compensation); err == nil && compensation.Compensated {
		if projection := cycles[event.CycleID]; projection != nil {
			projection.anomalies = append(projection.anomalies, AnomalyEvent{
				Type: event.Type, Sequence: event.Sequence,
				OccurredAt: event.OccurredAt,
				Summary:    anomalySummary(event.Type, ""),
			})
			projection.touch(event.Sequence)
			return
		}
	}
}

func (b *Builder) loadAll(ctx context.Context) (map[uuid.UUID]*transitProjection, error) {
	chambers, err := b.store.Chambers(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[uuid.UUID]*transitProjection)
	for _, chamberID := range chambers {
		cycles, plans, err := b.loadChamber(ctx, chamberID)
		if err != nil {
			return nil, err
		}
		for id, projection := range cycles {
			if plan, ok := plans[projection.planID]; ok {
				planCopy := plan
				projection.plan = &planCopy
			}
			result[id] = projection
		}
	}
	return result, nil
}

// evaluate applies the issuance gate to a projected transit.
func evaluate(p *transitProjection) *RejectReason {
	switch p.current.Stage {
	case model.StageCompleted:
		if p.current.Reason != handoffReason {
			return &RejectReason{
				Code:   RejectSignalMissing,
				Reason: "transit completed without a confirmed field signal handoff",
			}
		}
		if p.plan == nil {
			return &RejectReason{
				Code:   RejectPlanMissing,
				Reason: "completed transit has no recorded convoy plan",
			}
		}
		return nil
	case model.StageCancelled:
		reason := p.current.Reason
		if reason == "" && p.plan != nil {
			reason = p.plan.CancelNote
		}
		if reason == "" {
			reason = "transit was cancelled before completion"
		}
		return &RejectReason{Code: RejectCancelled, Reason: reason}
	case model.StageEmergency:
		reason := p.current.Reason
		if reason == "" {
			reason = "transit was terminated by an emergency stop"
		}
		return &RejectReason{Code: RejectEmergency, Reason: reason}
	default:
		return &RejectReason{
			Code:   RejectInProgress,
			Reason: "transit is still in progress at stage " + string(p.current.Stage),
		}
	}
}

// assemble converts an eligible projection into a signed certificate.
func assemble(p *transitProjection) *Certificate {
	devices := make([]DeviceConfirmation, 0, len(p.commandOrder))
	for _, id := range p.commandOrder {
		confirmation := p.commands[id]
		if confirmation == nil || confirmation.State != model.CommandSettled {
			continue
		}
		devices = append(devices, *confirmation)
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].SettledAt.Equal(devices[j].SettledAt) {
			return devices[i].Sequence < devices[j].Sequence
		}
		return devices[i].SettledAt.Before(devices[j].SettledAt)
	})

	anomalies := append([]AnomalyEvent(nil), p.anomalies...)
	sort.Slice(anomalies, func(i, j int) bool { return anomalies[i].Sequence < anomalies[j].Sequence })

	var admittedAt time.Time
	if len(p.admission) > 0 {
		admittedAt = p.admission[0].UpdatedAt
		for _, state := range p.admission[1:] {
			if state.UpdatedAt.Before(admittedAt) {
				admittedAt = state.UpdatedAt
			}
		}
	}
	if admittedAt.IsZero() {
		// Admission itself is not journaled as a signal event on every path;
		// the transition into "admitting" (the entry gate opened on a settled
		// command) is the durable evidence that the vessel entered the chamber.
		for _, stage := range p.stages {
			if stage.Stage == model.StageAdmitting {
				admittedAt = stage.OccurredAt
				break
			}
		}
	}
	releasedAt := p.current.UpdatedAt
	for _, stage := range p.stages {
		if stage.Stage == model.StageCompleted {
			releasedAt = stage.OccurredAt
		}
	}

	certificate := Certificate{
		Number:        "", // filled by sign()
		Fingerprint:   "",
		ChamberID:     p.chamberID,
		CycleID:       p.cycleID,
		PlanID:        p.planID,
		Generation:    p.generation,
		Direction:     p.direction,
		Stage:         model.StageCompleted,
		PlanCreatedAt: p.plan.CreatedAt,
		Vessels:       append([]model.Vessel(nil), p.plan.Vessels...),
		AdmittedAt:    admittedAt,
		ReleasedAt:    releasedAt,
		CompletedAt:   releasedAt,
		StageHistory:  append([]StageTime(nil), p.stages...),
		Devices:       devices,
		Anomalies:     anomalies,
		FirstSequence: p.firstSequence,
		LastSequence:  p.lastSequence,
	}
	sign(&certificate)
	return &certificate
}

func anomalySummary(kind model.EventType, detail string) string {
	switch kind {
	case model.EventPlanCancelled:
		if detail != "" {
			return "Convoy plan cancelled: " + detail
		}
		return "Convoy plan cancelled and compensated"
	case model.EventEmergencyStarted:
		if detail != "" {
			return "Emergency stop requested: " + detail
		}
		return "Emergency stop requested"
	case model.EventEmergencySettled:
		if detail != "" {
			return "Emergency equipment settled: " + detail
		}
		return "Emergency equipment reached stable state"
	case model.EventReplayWarning:
		return "Recovery replay warning requires operator review"
	case model.EventSignalChanged:
		return "Field signal publication compensated during cancellation"
	default:
		return string(kind)
	}
}
