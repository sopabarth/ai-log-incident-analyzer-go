// Package api is the HTTP face of the service: two routes, JSON in and out.
//
//	GET  /health            liveness check
//	POST /analyze-incident  analyse one log entry (see the README for the schema)
//
// It decodes and validates the request, hands it to the incident service, and
// maps the outcome to a status code. Errors are always {"detail": "..."}, like
// the Python service's FastAPI responses.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/incident"
)

const (
	// maxBodyBytes caps a request body; a log entry is text, not a file upload.
	maxBodyBytes = 1 << 20
	// analyzeTimeout bounds one analysis end to end, retries and backoff
	// waits included.
	analyzeTimeout = 2 * time.Minute

	// statusClientClosedRequest is nginx's de-facto status for "the client hung
	// up first". Nobody receives it; it keeps the request log honest.
	statusClientClosedRequest = 499
)

// Service analyses a log entry. *incident.Service implements it.
type Service interface {
	Analyze(ctx context.Context, entry domain.RawLogEntry) (domain.IncidentRecord, error)
}

// NewHandler returns the service's HTTP handler.
func NewHandler(svc Service, logger *slog.Logger) http.Handler {
	h := &handler{svc: svc, logger: logger}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("POST /analyze-incident", h.analyzeIncident)

	// Request logging wraps panic recovery, so a recovered panic is logged
	// with the 500 it produced.
	return logRequests(logger, recoverPanics(logger, mux))
}

type handler struct {
	svc    Service
	logger *slog.Logger
}

func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) analyzeIncident(w http.ResponseWriter, r *http.Request) {
	entry, reqErr := decodeEntry(w, r)
	if reqErr != nil {
		writeError(w, reqErr.status, reqErr.detail)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), analyzeTimeout)
	defer cancel()

	rec, err := h.svc.Analyze(ctx, entry)
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// writeFailure maps an error from the service to a response. Internal detail
// goes to the log, never to the client.
func (h *handler) writeFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, incident.ErrEmptyText):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		h.logger.Warn("analysis timed out", "error", err)
		writeError(w, http.StatusGatewayTimeout, "analysis timed out")
	case errors.Is(err, context.Canceled):
		w.WriteHeader(statusClientClosedRequest) // the client is gone; this is for the log
	case errors.Is(err, incident.ErrAnalysis):
		h.logger.Error("analysis failed", "error", err)
		writeError(w, http.StatusBadGateway, "the language model could not analyse this incident")
	default:
		h.logger.Error("request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

// requestError is a problem with the request itself.
type requestError struct {
	status int
	detail string
}

// decodeEntry reads and validates the request body: exactly one JSON object
// with every required field. Syntax problems are 400; a well-formed body with
// wrong or missing values is 422 (unprocessable), matching FastAPI.
func decodeEntry(w http.ResponseWriter, r *http.Request) (domain.RawLogEntry, *requestError) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))

	var entry domain.RawLogEntry
	if err := dec.Decode(&entry); err != nil {
		return domain.RawLogEntry{}, classifyDecodeError(err)
	}
	// Anything after the object (a second object, stray text) is an error too.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return domain.RawLogEntry{}, &requestError{http.StatusBadRequest, "request body must be a single JSON object"}
		}
		return domain.RawLogEntry{}, classifyDecodeError(err)
	}
	return entry, nil
}

func classifyDecodeError(err error) *requestError {
	var (
		tooLarge *http.MaxBytesError
		syntax   *json.SyntaxError
	)
	switch {
	case errors.As(err, &tooLarge):
		return &requestError{http.StatusRequestEntityTooLarge, "request body too large"}
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.As(err, &syntax):
		return &requestError{http.StatusBadRequest, "request body is not valid JSON"}
	default:
		// Valid JSON that does not fit RawLogEntry; the message names the
		// field and what was wrong with it.
		return &requestError{http.StatusUnprocessableEntity, err.Error()}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body) // if the client has gone, there is nobody to tell
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}
