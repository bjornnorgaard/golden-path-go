// Package paging resolves optional limit/offset request values into effective ones.
package paging

import "github.com/bjornnorgaard/golden-path-go/internal/domain"

// Options configures page sizes.
type Options struct {
	DefaultPageSize int32
	MaxPageSize     int32
}

// AbsoluteMaxLimit is the largest limit a request may ask for; it matches the
// maximum declared in the OpenAPI contracts. Larger requests are rejected, while
// requests above Options.MaxPageSize (but within this bound) are clamped.
const AbsoluteMaxLimit = 200

// Resolve validates the optional paging inputs and returns the effective limit and offset.
func Resolve(limit, offset *int32, o Options) (int32, int32, error) {
	badLimit := limit != nil && (*limit < 1 || *limit > AbsoluteMaxLimit)
	badOffset := offset != nil && *offset < 0
	if badLimit || badOffset {
		v := domain.ValidationError{}
		if badLimit {
			v.Add("limit", "limit must be between 1 and 200")
		}
		if badOffset {
			v.Add("offset", "offset must be zero or greater")
		}
		return 0, 0, v
	}
	return Clamp(limit, offset, o)
}

// Clamp applies defaults and the configured maximum without validating.
func Clamp(limit, offset *int32, o Options) (int32, int32, error) {
	l := o.DefaultPageSize
	if limit != nil {
		l = *limit
	}
	if l > o.MaxPageSize {
		l = o.MaxPageSize
	}
	var off int32
	if offset != nil {
		off = *offset
	}
	return l, off, nil
}
