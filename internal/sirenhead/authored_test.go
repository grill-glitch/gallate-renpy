package sirenhead

// Tests for the § 12.13 design principle: the fields a human authored
// (`target`, `state`, `context`, `notes`, `provenance`) MUST survive a
// re-extraction, and a translation whose source moved MUST NOT be
// carried into a position it no longer describes.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestReExtractPreservesAuthoredFields(t *testing.T) {
	h := newHarness(t)
	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	if n := h.setTargets(map[string]string{"Run away": "逃跑"}); n != 1 {
		t.Fatalf("expected 1 unit to translate, got %d", n)
	}
	// Add a translator note, which is also authored.
	doc := h.readJSON(filepath.Join(h.project, "text", "units", "script.json"))
	for _, e := range entries(t, doc) {
		if e["source"] == "Run away" {
			e["notes"] = []any{map[string]any{"text": "keep it short", "author": "translator"}}
			e["state"] = "reviewed"
		}
	}
	writeJSON(t, filepath.Join(h.project, "text", "units", "script.json"), doc)

	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("second extract exit=%d stderr=%s", code, stderr)
	}
	doc = h.readJSON(filepath.Join(h.project, "text", "units", "script.json"))
	found := false
	for _, e := range entries(t, doc) {
		if e["source"] != "Run away" {
			continue
		}
		found = true
		if e["target"] != "逃跑" {
			t.Errorf("target was lost on re-extraction: %v", e["target"])
		}
		if e["state"] != "reviewed" {
			t.Errorf("state was reset on re-extraction: %v", e["state"])
		}
		if _, ok := e["notes"]; !ok {
			t.Errorf("notes were dropped on re-extraction")
		}
	}
	if !found {
		t.Fatalf("the unit disappeared from the re-extracted document")
	}
	// The carried-over target really injects.
	if code, _, stderr := h.run("-it", h.gallateYAML()); code != ExitOK {
		t.Fatalf("inject exit=%d stderr=%s", code, stderr)
	}
	script := readBytes(t, filepath.Join(h.game, "script.rpy"))
	if !bytes.Contains(script, []byte("逃跑")) {
		t.Errorf("the restored translation was not injected")
	}
}

func TestReExtractDropsStaleTranslation(t *testing.T) {
	h := newHarness(t)
	script := filepath.Join(h.game, "script.rpy")
	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	if n := h.setTargets(map[string]string{"Run away": "逃跑"}); n != 1 {
		t.Fatalf("expected 1 unit to translate, got %d", n)
	}

	// Change the source at the SAME position: the id is unchanged, the
	// source is not, so the translation no longer describes this string.
	original := readBytes(t, script)
	modified := bytes.Replace(original, []byte(`"Run away":`), []byte(`"Walk away":`), 1)
	if bytes.Equal(original, modified) {
		t.Fatalf("fixture no longer contains the probe string")
	}
	if err := os.WriteFile(script, modified, 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := h.run("-et", h.gallateYAML())
	if code != ExitOK {
		t.Fatalf("re-extract exit=%d stderr=%s", code, stderr)
	}
	if !bytes.Contains([]byte(stderr), []byte("needs_review")) {
		t.Errorf("re-extract did not report the stale translation: %q", stderr)
	}
	doc := h.readJSON(filepath.Join(h.project, "text", "units", "script.json"))
	for _, e := range entries(t, doc) {
		if e["source"] != "Walk away" {
			continue
		}
		if e["target"] != "" {
			t.Errorf("a stale translation was carried over: %v", e["target"])
		}
		if e["state"] != "needs_review" {
			t.Errorf("stale entry state is %v, want needs_review", e["state"])
		}
	}
}
