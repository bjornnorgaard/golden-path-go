package paging

import "testing"

// These benchmarks mirror src/Benchmarks/PagingClampBenchmarks.cs in golden-path-aspnet
// (same three formulations, same inputs) so the two ecosystems can be compared directly.

const (
	defaultPage     = 1
	defaultPageSize = 20
	maxPageSize     = 100
)

type pagingValues struct{ page, pageSize int }

var (
	benchPage     *int
	benchPageSize = ptr(101)
	sink          pagingValues
)

func ptr(i int) *int { return &i }

func orDefault(p *int, d int) int {
	if p != nil {
		return *p
	}
	return d
}

func BenchmarkClampMinMax(b *testing.B) {
	for b.Loop() {
		sink = pagingValues{
			max(orDefault(benchPage, defaultPage), defaultPage),
			min(orDefault(benchPageSize, defaultPageSize), maxPageSize),
		}
	}
}

func BenchmarkClampConditionalOperators(b *testing.B) {
	for b.Loop() {
		page := orDefault(benchPage, defaultPage)
		pageSize := orDefault(benchPageSize, defaultPageSize)
		if page < defaultPage {
			page = defaultPage
		}
		if pageSize > maxPageSize {
			pageSize = maxPageSize
		}
		sink = pagingValues{page, pageSize}
	}
}

func BenchmarkClampIfStatements(b *testing.B) {
	for b.Loop() {
		page := orDefault(benchPage, defaultPage)
		if page < defaultPage {
			page = defaultPage
		}
		pageSize := orDefault(benchPageSize, defaultPageSize)
		if pageSize > maxPageSize {
			pageSize = maxPageSize
		}
		sink = pagingValues{page, pageSize}
	}
}

// BenchmarkResolve measures the production code path.
func BenchmarkResolve(b *testing.B) {
	limit, offset := int32(101), int32(0)
	o := Options{DefaultPageSize: 20, MaxPageSize: 100}
	for b.Loop() {
		_, _, _ = Resolve(&limit, &offset, o)
	}
}
