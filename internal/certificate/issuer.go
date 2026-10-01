package certificate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

// handoffReason is stamped by the cycle coordinator when a releasing transit
// completes through a confirmed field signal handoff.
const handoffReason = "field signal handoff confirmed"

// RejectionError reports why a transit cannot be certified.
type RejectionError struct {
	Stage  model.TransitStage
	Reason string
}

func (e *RejectionError) Error() string { return e.Reason }

// Issuer creates and retrieves transit certificates from the journal.
type Issuer struct {
	mu       sync.Mutex
	store    journal.Store
	recorder *journal.Recorder
	now      func() time.Time
}

func NewIssuer(store journal.Store, recorder *journal.Recorder, now func() time.Time) *Issuer {
	if now == nil {
		now = time.Now
	}
	return &Issuer{store: store, recorder: recorder, now: now}
}

// Entry is one row in the certificate console: issued info when present,
// otherwise current eligibility of the transit.
type Entry struct {
	CycleID     uuid.UUID       `json:"cycle_id"`
	ChamberID   string          `json:"chamber_id"`
	PlanID      uuid.UUID       `json:"plan_id"`
	Direction   model.Direction `json:"direction"`
	Stage       string          `json:"stage"`
	Generation  uint64          `json:"generation"`
	VesselCount int             `json:"vessel_count"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Eligible    bool            `json:"eligible"`
	Reason      string          `json:"reason,omitempty"`
	Certificate *Summary        `json:"certificate,omitempty"`
}

// List returns every known transit (across chambers) with its certificate
// status, newest first.
func (i *Issuer) List(ctx context.Context) ([]Entry, error) {
	all, err := i.loadAll(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0)
	for chamberID, records := range all {
		for cycleID, record := range records {
			if record.cycle.ID == uuid.Nil {
				continue // plan-only placeholder evidence
			}
			entry := Entry{
				CycleID: cycleID, ChamberID: chamberID, PlanID: record.cycle.PlanID,
				Direction: record.cycle.Direction, Stage: string(record.cycle.Stage),
				Generation: record.cycle.Generation, UpdatedAt: record.cycle.UpdatedAt,
				VesselCount: len(vesselsOf(record)),
			}
			if record.issued != nil {
				summary := record.issued.summary()
				entry.Certificate = &summary
			} else {
				entry.Eligible, entry.Reason = evaluate(record)
			}
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].UpdatedAt.After(entries[b].UpdatedAt) })
	return entries, nil
}

// Issue certifies one completed transit. Re-issuing an already certified
// transit is idempotent and returns the original certificate unchanged.
func (i *Issuer) Issue(ctx context.Context, cycleID uuid.UUID) (Certificate, bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	all, err := i.loadAll(ctx)
	if err != nil {
		return Certificate{}, false, err
	}
	record := findRecord(all, cycleID)
	if record == nil {
		return Certificate{}, false, ErrNotFound
	}
	if record.issued != nil {
		return *record.issued, true, nil
	}
	if eligible, reason := evaluate(record); !eligible {
		return Certificate{}, false, &RejectionError{Stage: record.cycle.Stage, Reason: reason}
	}

	sequence, err := i.nextSequence(ctx, record.chamberID)
	if err != nil {
		return Certificate{}, false, err
	}
	cert := buildCertificate(record, sequence, i.now().UTC())
	hash, err := contentHash(cert)
	if err != nil {
		return Certificate{}, false, err
	}
	cert.ContentHash = hash

	if _, err := i.recorder.Record(ctx, record.chamberID, model.EventCertificateIssued, cert.CycleID, cert.Generation, cert); err != nil {
		return Certificate{}, false, err
	}
	return cert, false, nil
}

// Get returns the issued certificate for a transit.
func (i *Issuer) Get(ctx context.Context, cycleID uuid.UUID) (Certificate, error) {
	all, err := i.loadAll(ctx)
	if err != nil {
		return Certificate{}, err
	}
	record := findRecord(all, cycleID)
	if record == nil {
		return Certificate{}, ErrNotFound
	}
	if record.issued == nil {
		return Certificate{}, ErrNotIssued
	}
	return *record.issued, nil
}

// Eligibility reports whether a transit can currently be certified.
func (i *Issuer) Eligibility(ctx context.Context, cycleID uuid.UUID) (Eligibility, error) {
	all, err := i.loadAll(ctx)
	if err != nil {
		return Eligibility{}, err
	}
	record := findRecord(all, cycleID)
	if record == nil {
		return Eligibility{}, ErrNotFound
	}
	result := Eligibility{CycleID: cycleID, Stage: string(record.cycle.Stage)}
	if record.issued != nil {
		result.Eligible = true
		result.Number = record.issued.Number
		return result, nil
	}
	result.Eligible, result.Reason = evaluate(record)
	return result, nil
}

func (i *Issuer) loadAll(ctx context.Context) (map[string]map[uuid.UUID]*transitRecord, error) {
	return (&projector{store: i.store}).allChambers(ctx)
}

func findRecord(all map[string]map[uuid.UUID]*transitRecord, cycleID uuid.UUID) *transitRecord {
	for _, records := range all {
		if record, ok := records[cycleID]; ok && record.cycle.ID != uuid.Nil {
			return record
		}
	}
	return nil
}

// evaluate enforces the issuance rule: genuinely completed transits with a
// confirmed field signal handoff; everything else gets an explicit reason.
func evaluate(record *transitRecord) (bool, string) {
	switch record.cycle.Stage {
	case model.StageCompleted:
		if !handoffConfirmed(record) {
			return false, "transit completed without a confirmed field signal handoff"
		}
		if record.plan == nil {
			return false, "convoy plan evidence is missing from the journal"
		}
		return true, ""
	case model.StageCancelled:
		reason := record.cycle.Reason
		if reason == "" {
			reason = "operator cancelled the transit before completion"
		}
		return false, "certificate refused: transit was cancelled (" + reason + ")"
	case model.StageEmergency:
		return false, "certificate refused: transit was terminated by an emergency stop"
	default:
		return false, fmt.Sprintf("certificate refused: transit is not complete (current stage %s)", record.cycle.Stage)
	}
}

func handoffConfirmed(record *transitRecord) bool {
	for _, transition := range record.transitions {
		if transition.Stage == model.StageCompleted && strings.Contains(transition.Reason, handoffReason) {
			return true
		}
	}
	return false
}

func vesselsOf(record *transitRecord) []model.Vessel {
	if record.plan != nil {
		return record.plan.Vessels
	}
	if record.issued != nil {
		return record.issued.Vessels
	}
	return nil
}

// nextSequence derives the per-chamber certificate serial from the number of
// certificate.issued events already persisted, so numbers never repeat.
func (i *Issuer) nextSequence(ctx context.Context, chamberID string) (uint64, error) {
	events, err := i.store.Events(ctx, chamberID, 0)
	if err != nil {
		return 0, err
	}
	var sequence uint64
	for _, event := range events {
		if event.Type == model.EventCertificateIssued {
			sequence++
		}
	}
	return sequence + 1, nil
}

func stageTime(record *transitRecord, stage model.TransitStage) time.Time {
	for _, transition := range record.transitions {
		if transition.Stage == stage {
			return transition.OccurredAt
		}
	}
	return time.Time{}
}

func buildCertificate(record *transitRecord, sequence uint64, issuedAt time.Time) Certificate {
	plan := record.plan
	admittedAt := stageTime(record, model.StageAdmitting)
	if admittedAt.IsZero() {
		admittedAt = record.cycle.CreatedAt
	}
	completedAt := stageTime(record, model.StageCompleted)
	incidents := append([]Incident(nil), record.incidents...)
	sort.SliceStable(incidents, func(a, b int) bool { return incidents[a].Sequence < incidents[b].Sequence })
	return Certificate{
		Number:          certificateNumber(record.chamberID, completedAt, sequence),
		ChamberID:       record.chamberID,
		CycleID:         record.cycle.ID,
		Generation:      record.cycle.Generation,
		PlanID:          plan.ID,
		Direction:       record.cycle.Direction,
		Vessels:         append([]model.Vessel(nil), plan.Vessels...),
		PlanCreatedAt:   plan.CreatedAt,
		AdmittedAt:      admittedAt,
		ReleasedAt:      stageTime(record, model.StageReleasing),
		CompletedAt:     completedAt,
		SignalHandoffAt: completedAt,
		Transitions:     append([]StageTransition(nil), record.transitions...),
		Devices:         record.deviceList(),
		Incidents:       incidents,
		EventRange:      [2]uint64{record.firstSeq, record.lastSeq},
		IssuedAt:        issuedAt,
	}
}

// certificateNumber is a human-readable unique serial, e.g. LFC-2026-C1-0007.
func certificateNumber(chamberID string, completedAt time.Time, sequence uint64) string {
	year := completedAt.Year()
	if year == 0 {
		year = time.Now().UTC().Year()
	}
	suffix := "X"
	for _, r := range chamberID {
		if r >= '0' && r <= '9' {
			suffix = "C" + string(r)
		}
	}
	return fmt.Sprintf("LFC-%d-%s-%04d", year, suffix, sequence)
}

func contentHash(cert Certificate) (string, error) {
	clone := cert
	clone.ContentHash = ""
	encoded, err := json.Marshal(clone)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
