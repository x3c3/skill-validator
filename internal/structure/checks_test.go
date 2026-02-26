package structure

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dacharyc/skill-validator/internal/validator"
)

func TestCheckStructure(t *testing.T) {
	t.Run("missing SKILL.md", func(t *testing.T) {
		dir := t.TempDir()
		results := CheckStructure(dir)
		requireResult(t, results, validator.Error, "SKILL.md not found")
	})

	t.Run("only SKILL.md", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "---\nname: test\n---\n")
		results := CheckStructure(dir)
		requireResult(t, results, validator.Pass, "SKILL.md found")
		requireNoLevel(t, results, validator.Error)
		requireNoLevel(t, results, validator.Warning)
	})

	t.Run("recognized directories", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
			t.Fatal(err)
		}
		results := CheckStructure(dir)
		requireResult(t, results, validator.Pass, "SKILL.md found")
		requireNoLevel(t, results, validator.Warning)
	})

	t.Run("unknown directory empty", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		if err := os.MkdirAll(filepath.Join(dir, "extras"), 0o755); err != nil {
			t.Fatal(err)
		}
		results := CheckStructure(dir)
		requireResult(t, results, validator.Info, "unknown directory: extras/")
	})

	t.Run("unknown directory with files suggests both dirs", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		writeFile(t, dir, "rules/rule1.md", "rule one")
		writeFile(t, dir, "rules/rule2.md", "rule two")
		writeFile(t, dir, "rules/rule3.md", "rule three")
		results := CheckStructure(dir)
		requireResultContaining(t, results, validator.Info, "unknown directory: rules/ (contains 3 files)")
		requireResultContaining(t, results, validator.Info, "won't discover these files")
		requireResultContaining(t, results, validator.Info, "should this be references/ or assets/?")
	})

	t.Run("unknown directory hint omits references when it exists", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, dir, "extras/file.md", "content")
		results := CheckStructure(dir)
		requireResultContaining(t, results, validator.Info, "should this be assets/?")
		requireNoResultContaining(t, results, validator.Info, "references/")
	})

	t.Run("unknown directory hint omits assets when it exists", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, dir, "extras/file.md", "content")
		results := CheckStructure(dir)
		requireResultContaining(t, results, validator.Info, "should this be references/?")
		requireNoResultContaining(t, results, validator.Info, "assets/")
	})

	t.Run("unknown directory hint omitted when both exist", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, dir, "extras/file.md", "content")
		results := CheckStructure(dir)
		requireResultContaining(t, results, validator.Info, "won't discover these files")
		requireNoResultContaining(t, results, validator.Info, "should this be")
	})

	t.Run("unknown directory with hidden files excluded from count", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		writeFile(t, dir, "extras/visible.md", "content")
		writeFile(t, dir, "extras/.hidden", "secret")
		results := CheckStructure(dir)
		requireResultContaining(t, results, validator.Info, "unknown directory: extras/ (contains 1 file)")
	})

	t.Run("AGENTS.md has specific warning", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		writeFile(t, dir, "AGENTS.md", "agent config")
		results := CheckStructure(dir)
		requireResultContaining(t, results, validator.Warning, "repo-level agent configuration")
		requireResultContaining(t, results, validator.Warning, "move it outside the skill directory")
	})

	t.Run("known extraneous file README.md", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		writeFile(t, dir, "README.md", "readme")
		results := CheckStructure(dir)
		requireResultContaining(t, results, validator.Info, "README.md is not part of the skill spec")
	})

	t.Run("known extraneous file CHANGELOG.md", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		writeFile(t, dir, "CHANGELOG.md", "changes")
		results := CheckStructure(dir)
		requireResultContaining(t, results, validator.Info, "CHANGELOG.md is not part of the skill spec")
	})

	t.Run("known extraneous file LICENSE", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		writeFile(t, dir, "LICENSE", "mit")
		results := CheckStructure(dir)
		requireResultContaining(t, results, validator.Info, "LICENSE is not part of the skill spec")
	})

	t.Run("unknown file at root", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		writeFile(t, dir, "notes.txt", "some notes")
		results := CheckStructure(dir)
		requireResultContaining(t, results, validator.Info, "extra file at root: notes.txt")
		requireResultContaining(t, results, validator.Info, "references/ or assets/")
	})

	t.Run("nested directory in recognized dir", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		if err := os.MkdirAll(filepath.Join(dir, "references", "subdir"), 0o755); err != nil {
			t.Fatal(err)
		}
		results := CheckStructure(dir)
		requireResult(t, results, validator.Info, "nested directory: references/subdir/")
	})

	t.Run("hidden files and dirs are skipped", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		writeFile(t, dir, ".hidden", "secret")
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		results := CheckStructure(dir)
		requireResult(t, results, validator.Pass, "SKILL.md found")
		requireNoLevel(t, results, validator.Warning)
	})

	t.Run("hidden dirs inside recognized dirs are skipped", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "SKILL.md", "content")
		if err := os.MkdirAll(filepath.Join(dir, "references", ".hidden"), 0o755); err != nil {
			t.Fatal(err)
		}
		results := CheckStructure(dir)
		requireNoLevel(t, results, validator.Warning)
	})
}
