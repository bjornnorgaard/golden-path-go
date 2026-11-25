package httpx_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bjornnorgaard/golden-path-go/internal/platform/httpx"
)

func TestRecover_TurnsPanicsIntoAProblemResponseWithoutLeakingDetails(t *testing.T) {
	h := httpx.Recover(slog.New(slog.DiscardHandler), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret internal detail")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("content type = %q", rec.Header().Get("Content-Type"))
	}
	if strings.Contains(rec.Body.String(), "secret internal detail") {
		t.Fatalf("response leaks panic detail: %s", rec.Body.String())
	}
}
