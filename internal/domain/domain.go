// Package domain holds the error vocabulary shared by services and transports.
//
// Services return these errors; transports translate them into whatever the
// contract documents (a 400/404 response, a GraphQL error code). Anything else
// is an unexpected failure and becomes a 500.
package domain

import (
	"errors"
	"sort"
	"strings"
)

// ErrNotFound means the addressed entity does not exist.
var ErrNotFound = errors.New("not found")

// ValidationError reports business-rule violations keyed by JSON property name.
type ValidationError map[string][]string

// Add records a violation for a property.
func (v ValidationError) Add(property, message string) {
	v[property] = append(v[property], message)
}

// OrNil returns nil when no violations were recorded, so callers can write
// `return v.OrNil()` without the typed-nil-in-interface trap.
func (v ValidationError) OrNil() error {
	if len(v) == 0 {
		return nil
	}
	return v
}

func (v ValidationError) Error() string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+strings.Join(v[k], "; "))
	}
	return "validation failed: " + strings.Join(parts, ", ")
}
