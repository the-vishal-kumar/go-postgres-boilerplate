package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
)

// apiError is an error with a client-facing status and message.
type apiError struct {
	Status  int
	Message string
	Errors  []string
}

func (e *apiError) Error() string { return e.Message }

func httpErr(status int, message string) *apiError {
	return &apiError{Status: status, Message: message}
}

// validationErr is the 422 answer; Errors lists every problem found.
func validationErr(problems []string) *apiError {
	return &apiError{Status: http.StatusUnprocessableEntity, Message: "Validation error", Errors: problems}
}

type envelope struct {
	Status int `json:"status"`
	Data   any `json:"data"`
}

type errorBody struct {
	Status    int      `json:"status"`
	Error     string   `json:"error"`
	Errors    []string `json:"errors,omitempty"`
	RequestID string   `json:"requestId,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status, b = http.StatusInternalServerError, []byte(`{"status":500,"error":"An error occurred"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func writeData(w http.ResponseWriter, status int, data any) {
	if data == nil {
		data = struct{}{}
	}
	writeJSON(w, status, envelope{Status: status, Data: data})
}

func writeNoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

func writeAPIError(w http.ResponseWriter, e *apiError) {
	writeJSON(w, e.Status, errorBody{Status: e.Status, Error: e.Message, Errors: e.Errors})
}

// reqState is the per-request data the middleware chain shares.
type reqState struct {
	id      string
	body    any
	hasBody bool
}

type ctxKey struct{}

func stateOf(ctx context.Context) *reqState {
	if s, ok := ctx.Value(ctxKey{}).(*reqState); ok {
		return s
	}
	return &reqState{}
}

// statusWriter records the status code for the access log.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
