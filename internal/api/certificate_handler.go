package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/wyw14/cry-96/internal/certificate"
)

// certificatesHandler lists every transit with its certificate eligibility.
// An optional ?chamber= filter restricts the result to one lock chamber.
func certificatesHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		summaries, err := runtime.Certificates.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if chamber := r.URL.Query().Get("chamber"); chamber != "" {
			filtered := summaries[:0]
			for _, summary := range summaries {
				if summary.ChamberID == chamber {
					filtered = append(filtered, summary)
				}
			}
			summaries = filtered
		}
		writeJSON(w, http.StatusOK, summaries)
	}
}

// certificateHandler issues (or refuses) the certificate for one transit.
func certificateHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cycleID, err := uuid.Parse(chi.URLParam(r, "cycleID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid transit cycle identifier"))
			return
		}
		issuance, err := runtime.Certificates.Issue(r.Context(), cycleID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if issuance.Reject != nil {
			writeError(w, rejectStatus(issuance.Reject.Code), errors.New(issuance.Reject.Reason))
			return
		}
		writeJSON(w, http.StatusOK, issuance)
	}
}

// certificateDownloadHandler serves the stable JSON evidence file. Rejected
// transits return the structured refusal instead of a file.
func certificateDownloadHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cycleID, err := uuid.Parse(chi.URLParam(r, "cycleID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid transit cycle identifier"))
			return
		}
		issuance, err := runtime.Certificates.Issue(r.Context(), cycleID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if issuance.Reject != nil {
			writeError(w, rejectStatus(issuance.Reject.Code), errors.New(issuance.Reject.Reason))
			return
		}
		encoded, err := certificate.RenderJSON(issuance.Certificate)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+certificate.DownloadName(issuance.Certificate, "json")+"\"")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(encoded)
	}
}

// certificatePageHandler renders the printable HTML certificate at the
// top-level /certificates/{cycleID} URL. Ineligible transits get an
// operator-facing refusal page carrying the reason.
func certificatePageHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cycleID, err := uuid.Parse(chi.URLParam(r, "cycleID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid transit cycle identifier"))
			return
		}
		issuance, err := runtime.Certificates.Issue(r.Context(), cycleID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if issuance.Reject != nil {
			page := certificate.RenderRefusalHTML(cycleID, issuance.Reject)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(rejectStatus(issuance.Reject.Code))
			_, _ = w.Write(page)
			return
		}
		page, err := certificate.RenderHTML(issuance.Certificate)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(page)
	}
}

// rejectStatus maps an eligibility code to an HTTP status.
func rejectStatus(code certificate.RejectCode) int {
	if code == certificate.RejectNotFound {
		return http.StatusNotFound
	}
	// Unfinished, cancelled or emergency transits are well-understood business
	// outcomes rather than server faults.
	return http.StatusConflict
}
