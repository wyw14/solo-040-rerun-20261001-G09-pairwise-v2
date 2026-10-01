// Package certificate issues non-repudiable transit certificates from the
// event journal and current business state. A certificate is only issued for
// transits that genuinely completed and whose field signal handoff was
// confirmed; cancelled, emergency or unfinished transits are rejected with a
// reason. Issuance itself is journaled so certificates are idempotent and
// survive process restarts.
package certificate

import (
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

// DeviceConfirmation records that a queued actuator command reached a settled
// terminal state during the transit.
type DeviceConfirmation struct {
	Device    model.DeviceKind   `json:"device"`
	Action    string             `json:"action"`
	Target    float64            `json:"target,omitempty"`
	State     model.CommandState `json:"state"`
	QueuedAt  time.Time          `json:"queued_at"`
	SettledAt *time.Time         `json:"settled_at,omitempty"`
	Sequence  uint64             `json:"sequence"`
}

// Incident is an exceptional event observed while the transit ran.
type Incident struct {
	Severity   string          `json:"severity"`
	Summary    string          `json:"summary"`
	EventType  model.EventType `json:"event_type"`
	Sequence   uint64          `json:"sequence"`
	OccurredAt time.Time       `json:"occurred_at"`
	Note       string          `json:"note,omitempty"`
}

// StageTransition marks one recorded phase of the transit timeline.
type StageTransition struct {
	Stage      model.TransitStage `json:"stage"`
	Reason     string             `json:"reason,omitempty"`
	Sequence   uint64             `json:"sequence"`
	OccurredAt time.Time          `json:"occurred_at"`
}

// Certificate is the signed payload describing one completed lock transit.
type Certificate struct {
	Number          string               `json:"number"`
	ContentHash     string               `json:"content_hash"`
	ChamberID       string               `json:"chamber_id"`
	CycleID         uuid.UUID            `json:"cycle_id"`
	Generation      uint64               `json:"generation"`
	PlanID          uuid.UUID            `json:"plan_id"`
	Direction       model.Direction      `json:"direction"`
	Vessels         []model.Vessel       `json:"vessels"`
	PlanCreatedAt   time.Time            `json:"plan_created_at"`
	AdmittedAt      time.Time            `json:"admitted_at"`
	ReleasedAt      time.Time            `json:"released_at"`
	CompletedAt     time.Time            `json:"completed_at"`
	SignalHandoffAt time.Time            `json:"signal_handoff_at"`
	Transitions     []StageTransition    `json:"transitions"`
	Devices         []DeviceConfirmation `json:"devices"`
	Incidents       []Incident           `json:"incidents"`
	EventRange      [2]uint64            `json:"event_range"`
	IssuedAt        time.Time            `json:"issued_at"`
}

// Summary is the list-row projection of an issued certificate.
type Summary struct {
	Number      string          `json:"number"`
	CycleID     uuid.UUID       `json:"cycle_id"`
	ChamberID   string          `json:"chamber_id"`
	Direction   model.Direction `json:"direction"`
	Generation  uint64          `json:"generation"`
	VesselCount int             `json:"vessel_count"`
	CompletedAt time.Time       `json:"completed_at"`
	IssuedAt    time.Time       `json:"issued_at"`
}

func (c Certificate) summary() Summary {
	return Summary{
		Number: c.Number, CycleID: c.CycleID, ChamberID: c.ChamberID,
		Direction: c.Direction, Generation: c.Generation,
		VesselCount: len(c.Vessels), CompletedAt: c.CompletedAt, IssuedAt: c.IssuedAt,
	}
}

// Eligibility explains whether a transit may be certified right now.
type Eligibility struct {
	CycleID  uuid.UUID `json:"cycle_id"`
	Eligible bool      `json:"eligible"`
	Stage    string    `json:"stage"`
	Reason   string    `json:"reason,omitempty"`
	Number   string    `json:"number,omitempty"`
}
