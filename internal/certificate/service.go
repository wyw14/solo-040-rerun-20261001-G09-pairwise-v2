package certificate

import (
	"context"
	"sort"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
)

// Service is the application boundary for querying and issuing certificates.
type Service struct {
	builder *Builder
}

// NewService wires a certificate service to the journal-backed builder.
func NewService(store journal.Store) *Service {
	return &Service{builder: NewBuilder(store)}
}

// Issue returns the certificate for a completed transit. Issuance is
// idempotent: every request for the same cycle projects the same evidence and
// therefore produces the same certificate number and fingerprint, so a
// certificate can never be duplicated. Cancelled, emergency or unfinished
// transits return a structured rejection instead.
func (s *Service) Issue(ctx context.Context, cycleID uuid.UUID) (Issuance, error) {
	projections, err := s.builder.loadAll(ctx)
	if err != nil {
		return Issuance{}, err
	}
	projection := projections[cycleID]
	if projection == nil {
		return Issuance{Reject: &RejectReason{
			Code: RejectNotFound, Reason: "no transit cycle found for this identifier",
		}}, nil
	}
	if rejection := evaluate(projection); rejection != nil {
		return Issuance{Reject: rejection}, nil
	}
	return Issuance{Certificate: assemble(projection)}, nil
}

// Get is an alias for Issue: issuance is a pure projection of the journal, so
// "viewing" and "(re-)issuing" a certificate are the same operation and always
// return the identical document.
func (s *Service) Get(ctx context.Context, cycleID uuid.UUID) (Issuance, error) {
	return s.Issue(ctx, cycleID)
}

// List returns every transit visible in the journal with its current issuance
// state, newest started first.
func (s *Service) List(ctx context.Context) ([]Summary, error) {
	projections, err := s.builder.loadAll(ctx)
	if err != nil {
		return nil, err
	}
	summaries := make([]Summary, 0, len(projections))
	for _, projection := range projections {
		summary := Summary{
			ChamberID: projection.chamberID, CycleID: projection.cycleID,
			PlanID: projection.planID, Generation: projection.generation,
			Direction: projection.direction, Stage: projection.current.Stage,
			Reason:    projection.current.Reason,
			StartedAt: projection.current.CreatedAt, UpdatedAt: projection.current.UpdatedAt,
		}
		if rejection := evaluate(projection); rejection == nil {
			summary.Certificate = assemble(projection)
		} else if rejection.Code != RejectInProgress {
			summary.Reject = rejection
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].StartedAt.Equal(summaries[j].StartedAt) {
			return summaries[i].CycleID.String() < summaries[j].CycleID.String()
		}
		return summaries[i].StartedAt.After(summaries[j].StartedAt)
	})
	return summaries, nil
}
