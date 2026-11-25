package config_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/bjornnorgaard/golden-path-go/internal/config"
)

func valid() map[string]string {
	return map[string]string{"DATABASE_URL": "postgres://x", "AUTH_SESSION_SECRET": strings.Repeat("s", 32)}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	c, err := config.LoadFrom(valid())
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" || c.PagingDefaultPageSize != 20 || c.PagingMaxPageSize != 100 || c.DocsEnabled || !c.JobsEnabled {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoad_ReportsEveryProblemAtOnce(t *testing.T) {
	_, err := config.LoadFrom(map[string]string{"PAGING_DEFAULT_PAGE_SIZE": "500"})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"DATABASE_URL", "AUTH_SESSION_SECRET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s: %v", want, err)
		}
	}
}

func TestLoad_RejectsShortSecretAndInconsistentPaging(t *testing.T) {
	env := valid()
	env["AUTH_SESSION_SECRET"] = "short"
	env["PAGING_DEFAULT_PAGE_SIZE"] = "500"
	_, err := config.LoadFrom(env)
	if err == nil || !strings.Contains(err.Error(), "AUTH_SESSION_SECRET") || !strings.Contains(err.Error(), "PAGING_DEFAULT_PAGE_SIZE") {
		t.Fatalf("want both problems reported, got %v", err)
	}
}

// docs/configuration.md is generated from the struct tags; it must never drift.
func TestConfigurationDocsAreUpToDate(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "run", "../../tools/cmd/gen-config-docs").Output()
	if err != nil {
		t.Fatal(err)
	}
	have, err := os.ReadFile("../../docs/configuration.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(have) {
		t.Fatal("docs/configuration.md is stale: run `make generate`")
	}
}
