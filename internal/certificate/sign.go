package certificate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/wyw14/cry-96/internal/model"
)

// sign stamps a certificate with its deterministic number and content
// fingerprint. Everything covered by the fingerprint comes from durable
// journal evidence, so issuing the same transit twice yields the identical
// certificate number, fingerprint and bytes — duplicates are impossible.
func sign(c *Certificate) {
	c.IssuedAt = c.CompletedAt.UTC()
	payload := fingerprintPayload{
		ChamberID:     c.ChamberID,
		CycleID:       c.CycleID.String(),
		PlanID:        c.PlanID.String(),
		Generation:    c.Generation,
		Direction:     string(c.Direction),
		Stage:         string(c.Stage),
		PlanCreatedAt: c.PlanCreatedAt,
		Vessels:       c.Vessels,
		AdmittedAt:    c.AdmittedAt,
		ReleasedAt:    c.ReleasedAt,
		CompletedAt:   c.CompletedAt,
		StageHistory:  c.StageHistory,
		Devices:       c.Devices,
		Anomalies:     c.Anomalies,
		FirstSequence: c.FirstSequence,
		LastSequence:  c.LastSequence,
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	c.Fingerprint = hex.EncodeToString(digest[:])
	c.Number = certificateNumber(c.ChamberID, c.Generation, c.Fingerprint)
}

type fingerprintPayload struct {
	ChamberID     string               `json:"chamber_id"`
	CycleID       string               `json:"cycle_id"`
	PlanID        string               `json:"plan_id"`
	Generation    uint64               `json:"generation"`
	Direction     string               `json:"direction"`
	Stage         string               `json:"stage"`
	PlanCreatedAt time.Time            `json:"plan_created_at"`
	Vessels       []model.Vessel       `json:"vessels"`
	AdmittedAt    time.Time            `json:"admitted_at"`
	ReleasedAt    time.Time            `json:"released_at"`
	CompletedAt   time.Time            `json:"completed_at"`
	StageHistory  []StageTime          `json:"stage_history"`
	Devices       []DeviceConfirmation `json:"devices"`
	Anomalies     []AnomalyEvent       `json:"anomalies"`
	FirstSequence uint64               `json:"first_sequence"`
	LastSequence  uint64               `json:"last_sequence"`
}

// certificateNumber builds a stable human-readable code:
// LFC-<CHAMBER>-G<generation>-<10 fingerprint hex digits>.
func certificateNumber(chamberID string, generation uint64, fingerprint string) string {
	var builder strings.Builder
	for _, r := range strings.ToUpper(chamberID) {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') {
			builder.WriteRune(r)
		}
	}
	short := strings.ToUpper(fingerprint)
	if len(short) > 10 {
		short = short[:10]
	}
	return "LFC-" + builder.String() + "-G" + strconv.FormatUint(generation, 10) + "-" + short
}
