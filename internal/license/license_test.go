package license

import (
	"os"
	"path/filepath"
	"testing"
)

func TestActivateLoadRemove(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st, err := Activate("thin_test_1234567890")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if !st.Licensed || st.Source != "file" || st.Path == "" {
		t.Fatalf("unexpected status: %+v", st)
	}
	if _, err := os.Stat(st.Path); err != nil {
		t.Fatalf("stat license file: %v", err)
	}

	rec, err := Load(st.Path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if rec.Key != "thin_test_1234567890" {
		t.Fatalf("key = %q", rec.Key)
	}

	removed, err := Remove()
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if removed != st.Path {
		t.Fatalf("removed path = %q, want %q", removed, st.Path)
	}
	if _, err := os.Stat(st.Path); !os.IsNotExist(err) {
		t.Fatalf("license file still exists or unexpected stat err: %v", err)
	}
}

func TestCurrentPrefersEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := Activate("thin_file_1234567890"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	t.Setenv(EnvKey, "thin_env_1234567890")

	st := Current()
	if !st.Licensed || st.Source != "environment" {
		t.Fatalf("unexpected status: %+v", st)
	}
	if st.Path != "" {
		t.Fatalf("environment status should not expose file path, got %q", st.Path)
	}
}

func TestCurrentUnlicensedIncludesDefaultPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	st := Current()
	if st.Licensed {
		t.Fatalf("expected unlicensed status: %+v", st)
	}
	want, err := DefaultPath()
	if err != nil {
		t.Fatalf("default path: %v", err)
	}
	if !filepath.IsAbs(want) {
		t.Fatalf("default path is not absolute: %q", want)
	}
	if st.Path != want {
		t.Fatalf("path = %q, want %q", st.Path, want)
	}
}
