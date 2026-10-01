package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/certificate"
)

func certificatesHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries, err := runtime.Certificates.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, entries)
	}
}

func issueCertificateHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cycleID, err := uuid.Parse(chi.URLParam(r, "cycleID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cert, already, err := runtime.Certificates.Issue(r.Context(), cycleID)
		if err != nil {
			writeCertificateError(w, err)
			return
		}
		status := http.StatusCreated
		if already {
			status = http.StatusOK
		}
		writeJSON(w, status, map[string]any{"certificate": cert, "already_issued": already})
	}
}

func certificateDetailHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cycleID, err := uuid.Parse(chi.URLParam(r, "cycleID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cert, err := runtime.Certificates.Get(r.Context(), cycleID)
		if err != nil {
			writeCertificateError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, cert)
	}
}

func certificateDocumentHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cycleID, err := uuid.Parse(chi.URLParam(r, "cycleID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cert, err := runtime.Certificates.Get(r.Context(), cycleID)
		if err != nil {
			writeCertificateError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Query().Get("format") == "json" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="`+jsonDownloadName(cert)+`"`)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(cert)
			return
		}
		document, err := certificate.Document(cert)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+certificate.DownloadName(cert)+`"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(document)
	}
}

func jsonDownloadName(cert certificate.Certificate) string {
	return strings.TrimSuffix(certificate.DownloadName(cert), ".html") + ".json"
}

func writeCertificateError(w http.ResponseWriter, err error) {
	var rejection *certificate.RejectionError
	if errors.As(err, &rejection) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  rejection.Reason,
			"stage":  string(rejection.Stage),
			"reason": rejection.Reason,
		})
		return
	}
	switch {
	case errors.Is(err, certificate.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, certificate.ErrNotIssued):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}
