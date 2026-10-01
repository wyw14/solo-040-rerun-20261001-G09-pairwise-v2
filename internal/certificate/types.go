// Package certificate issues non-repudiable, non-duplicable electronic
// certificates for lock transits. Certificates are projected strictly from
// durable journal events and the current terminal business state: only a
// genuinely completed transit whose completion was driven by a confirmed
// field signal handoff is eligible.
package certificate

import (
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

// RejectCode explains why a transit cannot hold a certificate.
type RejectCode string

const (
	// RejectNotFound means no transit cycle exists for the lookup.
	RejectNotFound RejectCode = "cycle_not_found"
	// RejectInProgress means the transit has not reached a terminal stage.
	RejectInProgress RejectCode = "transit_in_progress"
	// RejectCancelled means the transit (or its plan) was cancelled.
	RejectCancelled RejectCode = "transit_cancelled"
	// RejectEmergency means the transit was terminated by an emergency stop.
	RejectEmergency RejectCode = "emergency_terminated"
	// RejectSignalMissing means a completed transit lacks a confirmed handoff.
	RejectSignalMissing RejectCode = "signal_handoff_unconfirmed"
	// RejectPlanMissing means the completed cycle has no recorded convoy plan.
	RejectPlanMissing RejectCode = "plan_not_found"
)

// RejectReason carries a machine-readable code and an operator-facing reason.
type RejectReason struct {
	Code   RejectCode `json:"code"`
	Reason string     `json:"reason"`
}

// StageTime records when the transit entered a given lifecycle stage.
type StageTime struct {
	Stage      model.TransitStage `json:"stage"`
	OccurredAt time.Time          `json:"occurred_at"`
	Sequence   uint64             `json:"sequence"`
}

// DeviceConfirmation is a settled device command that the field acknowledged.
type DeviceConfirmation struct {
	CommandID uuid.UUID          `json:"command_id"`
	Device    model.DeviceKind   `json:"device"`
	Action    string             `json:"action"`
	Target    float64            `json:"target,omitempty"`
	State     model.CommandState `json:"state"`
	IssuedAt  time.Time          `json:"issued_at"`
	SettledAt time.Time          `json:"settled_at"`
	Sequence  uint64             `json:"sequence"`
}

// AnomalyEvent is an exceptional occurrence recorded against the transit.
type AnomalyEvent struct {
	Type       model.EventType `json:"type"`
	Sequence   uint64          `json:"sequence"`
	OccurredAt time.Time       `json:"occurred_at"`
	Summary    string          `json:"summary"`
}

// Certificate is the projected, signed evidence of a finished transit.
type Certificate struct {
	// Number is the stable, human-readable certificate identifier. Re-issuing
	// the same transit always yields the same number, so duplicates cannot be
	// created.
	Number string `json:"number"`
	// Fingerprint is the SHA-256 content digest of the certified evidence.
	Fingerprint string `json:"fingerprint"`

	ChamberID  string             `json:"chamber_id"`
	CycleID    uuid.UUID          `json:"cycle_id"`
	PlanID     uuid.UUID          `json:"plan_id"`
	Generation uint64             `json:"generation"`
	Direction  model.Direction    `json:"direction"`
	Stage      model.TransitStage `json:"stage"`

	PlanCreatedAt time.Time      `json:"plan_created_at"`
	Vessels       []model.Vessel `json:"vessels"`

	// AdmittedAt is the durable entry evidence: the first proceed signal for
	// a convoy vessel, or the transition into "admitting" when admission did
	// not emit a signal event. ReleasedAt is the confirmed field signal
	// handoff that completed the transit.
	AdmittedAt  time.Time `json:"admitted_at"`
	ReleasedAt  time.Time `json:"released_at"`
	CompletedAt time.Time `json:"completed_at"`

	StageHistory []StageTime          `json:"stage_history"`
	Devices      []DeviceConfirmation `json:"devices"`
	Anomalies    []AnomalyEvent       `json:"anomalies"`

	// EventRange spans every journal event certified by this document.
	FirstSequence uint64 `json:"first_sequence"`
	LastSequence  uint64 `json:"last_sequence"`

	IssuedAt time.Time `json:"issued_at"`
}

// Issuance is the result of requesting a certificate: exactly one of
// Certificate or Reject is populated.
type Issuance struct {
	Certificate *Certificate  `json:"certificate,omitempty"`
	Reject      *RejectReason `json:"reject,omitempty"`
}

// Summary describes a discovered transit for listing.
type Summary struct {
	ChamberID   string             `json:"chamber_id"`
	CycleID     uuid.UUID          `json:"cycle_id"`
	PlanID      uuid.UUID          `json:"plan_id"`
	Generation  uint64             `json:"generation"`
	Direction   model.Direction    `json:"direction"`
	Stage       model.TransitStage `json:"stage"`
	Reason      string             `json:"reason,omitempty"`
	StartedAt   time.Time          `json:"started_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	Certificate *Certificate       `json:"certificate,omitempty"`
	Reject      *RejectReason      `json:"reject,omitempty"`
}
