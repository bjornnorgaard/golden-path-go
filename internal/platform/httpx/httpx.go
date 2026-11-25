// Package httpx holds HTTP plumbing shared by the transports: error responses,
// panic recovery and contract-driven request validation.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteValidation writes the contract's ValidationErrors body with status 400.
func WriteValidation(w http.ResponseWriter, errs map[string][]string) {
	WriteJSON(w, http.StatusBadRequest, errs)
}

// WriteProblem writes an RFC 9457 problem document including the trace id, so a
// failing response can be correlated with its trace and logs.
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, title, detail string) {
	body := map[string]any{"title": title, "status": status, "detail": detail}
	if sc := trace.SpanContextFromContext(r.Context()); sc.IsValid() {
		body["traceId"] = sc.TraceID().String()
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Unexpected records an unexpected failure on the request's span and log, and
// answers with a generic 500 that leaks nothing.
func Unexpected(log *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	span := trace.SpanFromContext(r.Context())
	span.RecordError(err)
	span.SetStatus(codes.Error, "unhandled error")
	log.ErrorContext(r.Context(), "unhandled error", "method", r.Method, "path", r.URL.Path, "error", err)
	WriteProblem(w, r, http.StatusInternalServerError, "Unhandled exception", "An unexpected error occurred.")
}

// Recover turns panics into the same 500 problem response as other unexpected errors.
func Recover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				Unexpected(log, w, r, panicError{value: rec, stack: debug.Stack()})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type panicError struct {
	value any
	stack []byte
}

func (p panicError) Error() string { return "panic: " + toString(p.value) + "\n" + string(p.stack) }

func toString(v any) string {
	switch t := v.(type) {
	case error:
		return t.Error()
	case string:
		return t
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// RESTHandlers are the error hooks plugged into every generated strict server.
type RESTHandlers struct {
	Log *slog.Logger
	// ValidateRequests enables contract validation of every request (see ContractValidator).
	ValidateRequests bool
}

// Validators returns the validation middleware for spec, or none when validation is disabled.
func (h RESTHandlers) Validators(spec *openapi3.T) []func(http.Handler) http.Handler {
	if !h.ValidateRequests {
		return nil
	}
	return []func(http.Handler) http.Handler{ContractValidator(spec)}
}

// RequestError handles bodies that cannot be decoded.
func (h RESTHandlers) RequestError(w http.ResponseWriter, _ *http.Request, err error) {
	WriteValidation(w, map[string][]string{"body": {err.Error()}})
}

// ResponseError handles errors returned by operation implementations (anything undocumented).
func (h RESTHandlers) ResponseError(w http.ResponseWriter, r *http.Request, err error) {
	Unexpected(h.Log, w, r, err)
}

// ContractValidator validates every request against the OpenAPI contract
// (required fields, types, patterns, bounds) before it reaches an operation.
// Violations become the contract's documented 400 ValidationErrors response.
func ContractValidator(spec *openapi3.T) func(http.Handler) http.Handler {
	return nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			// Authentication is enforced by auth.Middleware before routing; the contract's
			// security scheme documents it, so the validator must not re-check it.
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
			MultiError:         true,
		},
		SilenceServersWarning: true,
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, opts nethttpmiddleware.ErrorHandlerOpts) {
			if opts.MatchedRoute == nil {
				http.NotFound(w, nil)
				return
			}
			WriteValidation(w, ValidationErrorsFrom(err))
		},
	})
}

// ValidationErrorsFrom flattens a kin-openapi validation error into property -> messages.
func ValidationErrorsFrom(err error) map[string][]string {
	out := map[string][]string{}
	var collect func(error)
	collect = func(err error) {
		var multi openapi3.MultiError
		var reqErr *openapi3filter.RequestError
		var schemaErr *openapi3.SchemaError
		switch {
		case errors.As(err, &multi):
			for _, e := range multi {
				collect(e)
			}
		case errors.As(err, &schemaErr):
			field := "body"
			if p := schemaErr.JSONPointer(); len(p) > 0 {
				field = p[len(p)-1]
			}
			out[field] = append(out[field], schemaErr.Reason)
		case errors.As(err, &reqErr):
			field := "body"
			if reqErr.Parameter != nil {
				field = reqErr.Parameter.Name
			}
			msg := reqErr.Reason
			if msg == "" && reqErr.Err != nil {
				msg = reqErr.Err.Error()
			}
			out[field] = append(out[field], strings.TrimSpace(msg))
		default:
			out["body"] = append(out["body"], err.Error())
		}
	}
	collect(err)
	return out
}
