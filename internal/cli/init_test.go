package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akhanatoon/quench/internal/config"
)

func TestInitGeneratesValidV013TemplateGeneric(t *testing.T) {
	tmp := t.TempDir()
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"-C", tmp, "init"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("quench init failed: %v\n%s", err, out.String())
	}

	raw, err := os.ReadFile(filepath.Join(tmp, config.ConfigFileName))
	if err != nil {
		t.Fatalf("read quench.yaml: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "default_timeout: 120s") {
		t.Fatalf("expected default_timeout: 120s in generated config:\n%s", text)
	}
	if !strings.Contains(text, "origin/main:quench.yaml") {
		t.Fatalf("expected --config hint in header:\n%s", text)
	}

	cfg, _, err := config.LoadFrom(tmp, "")
	if err != nil {
		t.Fatalf("generated config failed validation: %v", err)
	}
	if cfg.DefaultTimeout != "120s" {
		t.Fatalf("expected DefaultTimeout=120s, got %q", cfg.DefaultTimeout)
	}
}

func TestInitGeneratesValidV013TemplateGo(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module example.com/demo\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"-C", tmp, "init"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("quench init (go) failed: %v\n%s", err, out.String())
	}

	cfg, _, err := config.LoadFrom(tmp, "")
	if err != nil {
		t.Fatalf("generated Go config failed validation: %v", err)
	}
	if cfg.DefaultTimeout != "120s" {
		t.Fatalf("expected DefaultTimeout=120s, got %q", cfg.DefaultTimeout)
	}
}
