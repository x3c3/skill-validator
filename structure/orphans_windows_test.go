//go:build windows

package structure

import (
	"strings"
	"testing"

	"github.com/agent-ecosystem/skill-validator/types"
)

func TestInventoryFilesUsesForwardSlashes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "references/guide.md", "guide content")
	writeFile(t, dir, "references/images/diagram.png", "fake image")
	writeFile(t, dir, "scripts/setup.sh", "#!/bin/bash")

	inventory := inventoryFiles(dir)
	for _, rel := range inventory {
		if strings.Contains(rel, `\`) {
			t.Errorf("inventory path contains backslash: %s", rel)
		}
	}
}

func TestOrphanCheckWithForwardSlashReferences(t *testing.T) {
	// This is the exact scenario from issue #63: skill author writes
	// forward-slash paths in SKILL.md, which is the cross-platform convention.
	// On Windows, filepath.WalkDir returns backslash paths, so the orphan
	// checker must normalize before comparing.
	dir := t.TempDir()
	writeFile(t, dir, "references/other.md", "reference content")

	body := "See references/other.md."
	results := CheckOrphanFiles(dir, body, Options{})

	requireResult(t, results, types.Pass, "all files in references/ are referenced")
	requireNoLevel(t, results, types.Warning)
}

func TestOrphanCheckNestedForwardSlashReference(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "references/guide.md", "See ![diagram](images/diagram.png).")
	writeFile(t, dir, "references/images/diagram.png", "fake image")

	body := "Read the [guide](references/guide.md)."
	results := CheckOrphanFiles(dir, body, Options{})

	requireNoResultContaining(t, results, types.Warning, "diagram.png")
	requireResult(t, results, types.Pass, "all files in references/ are referenced")
}

func TestPythonImportResolvesOnWindows(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "scripts/main.py", "from helpers.merge_runs import merge\nmerge()")
	writeFile(t, dir, "scripts/helpers/__init__.py", "")
	writeFile(t, dir, "scripts/helpers/merge_runs.py", "def merge(): pass")

	body := "Run scripts/main.py to start."
	results := CheckOrphanFiles(dir, body, Options{})

	requireNoResultContaining(t, results, types.Warning, "merge_runs.py")
	requireResult(t, results, types.Pass, "all files in scripts/ are referenced")
}
