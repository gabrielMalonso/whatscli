package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDocsCommandPrintsDocsURL(t *testing.T) {
	out := captureRootStdout(t, func() {
		if err := execute([]string{"docs"}); err != nil {
			t.Fatalf("execute docs: %v", err)
		}
	})

	if strings.TrimSpace(out) != docsURL {
		t.Fatalf("docs output = %q, want %q", out, docsURL)
	}
}

func TestDocsCommandJSON(t *testing.T) {
	out := captureRootStdout(t, func() {
		if err := execute([]string{"--json", "docs"}); err != nil {
			t.Fatalf("execute docs --json: %v", err)
		}
	})

	var got struct {
		Success bool `json:"success"`
		Data    struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("docs JSON = %q: %v", out, err)
	}
	if !got.Success || got.Data.URL != docsURL {
		t.Fatalf("docs JSON = %+v, want url %q", got, docsURL)
	}
}

func TestRootHelpShowsDocsURL(t *testing.T) {
	out := captureRootStdout(t, func() {
		if err := execute([]string{"--help"}); err != nil {
			t.Fatalf("execute --help: %v", err)
		}
	})

	if !strings.Contains(out, docsURL) {
		t.Fatalf("root help did not include docs URL: %q", out)
	}
	if !strings.Contains(out, "docs") {
		t.Fatalf("root help did not include docs command: %q", out)
	}
}
