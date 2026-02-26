package structure

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dacharyc/skill-validator/internal/validator"
)

var recognizedDirs = map[string]bool{
	"scripts":    true,
	"references": true,
	"assets":     true,
}

// Files commonly found in repos but not intended for agent consumption.
// Per Anthropic best practices: "A skill should only contain essential files
// that directly support its functionality."
// See: github.com/anthropics/skills → skill-creator
var knownExtraneousFiles = map[string]string{
	"readme.md":             "README.md",
	"readme":                "README",
	"changelog.md":          "CHANGELOG.md",
	"changelog":             "CHANGELOG",
	"license":               "LICENSE",
	"license.md":            "LICENSE.md",
	"license.txt":           "LICENSE.txt",
	"contributing.md":       "CONTRIBUTING.md",
	"code_of_conduct.md":    "CODE_OF_CONDUCT.md",
	"installation_guide.md": "INSTALLATION_GUIDE.md",
	"quick_reference.md":    "QUICK_REFERENCE.md",
	"makefile":              "Makefile",
	".gitignore":            ".gitignore",
}

func CheckStructure(dir string) []validator.Result {
	var results []validator.Result

	// Check SKILL.md exists
	skillPath := filepath.Join(dir, "SKILL.md")
	if _, err := os.Stat(skillPath); os.IsNotExist(err) {
		results = append(results, validator.Result{Level: validator.Error, Category: "Structure", Message: "SKILL.md not found"})
		return results
	}
	results = append(results, validator.Result{Level: validator.Pass, Category: "Structure", Message: "SKILL.md found"})

	// Check directories
	entries, err := os.ReadDir(dir)
	if err != nil {
		results = append(results, validator.Result{Level: validator.Error, Category: "Structure", Message: fmt.Sprintf("reading directory: %v", err)})
		return results
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue // skip hidden files/dirs
		}
		if !entry.IsDir() {
			if name != "SKILL.md" {
				results = append(results, extraneousFileResult(name))
			}
			continue
		}
		if !recognizedDirs[name] {
			// The spec defines scripts/, references/, and assets/ as optional
			// directories but doesn't prohibit others. Use Info level since
			// this is guidance, not a spec violation.
			msg := fmt.Sprintf("unknown directory: %s/", name)
			if subEntries, err := os.ReadDir(filepath.Join(dir, name)); err == nil {
				fileCount := 0
				for _, se := range subEntries {
					if !strings.HasPrefix(se.Name(), ".") {
						fileCount++
					}
				}
				if fileCount > 0 {
					hint := unknownDirHint(dir)
					msg = fmt.Sprintf(
						"unknown directory: %s/ (contains %d file%s) — agents using the standard skill structure won't discover these files%s",
						name, fileCount, pluralS(fileCount), hint,
					)
				}
			}
			results = append(results, validator.Result{Level: validator.Info, Category: "Structure", Message: msg})
		}
	}

	// Check for deep nesting in recognized directories
	for dirName := range recognizedDirs {
		subdir := filepath.Join(dir, dirName)
		if _, err := os.Stat(subdir); os.IsNotExist(err) {
			continue
		}
		err := checkNesting(subdir, dirName)
		if err != nil {
			results = append(results, err...)
		}
	}

	return results
}

func extraneousFileResult(name string) validator.Result {
	lower := strings.ToLower(name)
	if lower == "agents.md" {
		// AGENTS.md is genuinely misplaced — it's repo-level config that won't
		// work inside a skill directory. Keep as Warning.
		return validator.Result{
			Level:    validator.Warning,
			Category: "Structure",
			Message: fmt.Sprintf(
				"%s is for repo-level agent configuration, not skill content — "+
					"move it outside the skill directory (e.g. to the repository root) "+
					"where agents discover it automatically",
				name,
			),
		}
	}
	// The spec only requires SKILL.md and doesn't restrict other root files.
	// Emit Info-level notices so authors are aware, but don't treat these as
	// validation problems.
	if _, known := knownExtraneousFiles[lower]; known {
		return validator.Result{
			Level:    validator.Info,
			Category: "Structure",
			Message: fmt.Sprintf(
				"%s is not part of the skill spec — agents may load it into their context window, "+
					"consider whether it directly supports agent functionality",
				name,
			),
		}
	}
	return validator.Result{
		Level:    validator.Info,
		Category: "Structure",
		Message: fmt.Sprintf(
			"extra file at root: %s — if agents need this file, consider moving it into "+
				"references/ or assets/ so it follows the progressive disclosure pattern",
			name,
		),
	}
}

func unknownDirHint(dir string) string {
	var candidates []string
	if _, err := os.Stat(filepath.Join(dir, "references")); os.IsNotExist(err) {
		candidates = append(candidates, "references/")
	}
	if _, err := os.Stat(filepath.Join(dir, "assets")); os.IsNotExist(err) {
		candidates = append(candidates, "assets/")
	}
	if len(candidates) == 0 {
		return ""
	}
	return fmt.Sprintf("; should this be %s?", strings.Join(candidates, " or "))
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func checkNesting(dir, prefix string) []validator.Result {
	var results []validator.Result
	entries, err := os.ReadDir(dir)
	if err != nil {
		return results
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.IsDir() {
			// The spec says "Avoid deeply nested reference chains" which is
			// guidance about reference depth, not directory structure. Subdirs
			// in recognized dirs are fine; use Info to note them without
			// treating them as a validation problem.
			results = append(results, validator.Result{
				Level:    validator.Info,
				Category: "Structure",
				Message:  fmt.Sprintf("nested directory: %s/%s/", prefix, entry.Name()),
			})
		}
	}
	return results
}
