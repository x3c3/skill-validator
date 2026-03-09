# skill-validator: A Code Walkthrough

*2026-03-09T04:50:52Z by Showboat 0.6.1*
<!-- showboat-id: ece8fa30-7840-47c2-8313-b600380c5d07 -->
<!-- Note: showboat verify fails on this document because Go source code
     output contains triple-backtick regex patterns that break fence parsing.
     The document renders correctly as markdown. -->

## What is skill-validator?

skill-validator is a CLI tool and Go library that validates **Agent Skill packages** — the structured markdown bundles that give AI coding agents (Claude Code, Copilot, Cursor, etc.) specialized capabilities. It checks spec compliance, content quality, cross-language contamination, external links, and can even score skills via LLM APIs.

The project is well-factored into ~14 top-level packages with clear responsibilities. Let's trace how it works from entry point to output.

---

## Project Structure

```bash
find . -type f -name "*.go" \! -path "./.git/*" \! -name "*_test.go" | sort | sed "s|^\./||"
```

```output
cmd/analyze_contamination.go
cmd/analyze_content.go
cmd/analyze.go
cmd/check.go
cmd/exitcode.go
cmd/root.go
cmd/score_evaluate.go
cmd/score_report.go
cmd/score.go
cmd/skill-validator/main.go
cmd/validate_links.go
cmd/validate_structure.go
cmd/validate.go
contamination/contamination.go
content/content.go
doc.go
evaluate/evaluate.go
judge/cache.go
judge/client.go
judge/judge.go
links/check.go
links/extract.go
orchestrate/orchestrate.go
report/annotations.go
report/eval_cached.go
report/eval.go
report/json.go
report/markdown.go
report/report.go
skill/skill.go
skillcheck/validator.go
structure/checks.go
structure/frontmatter.go
structure/links.go
structure/markdown.go
structure/orphans.go
structure/tokens.go
structure/validate.go
types/context.go
types/types.go
util/util.go
```

41 source files across 14 packages. Here's the dependency flow:

```bash
cat <<'DIAGRAM'
CLI (cmd/) → Orchestrate → Structure / Links / Content / Contamination
                         → Evaluate → Judge (LLM clients) → Cache
                         → Report (text / JSON / markdown / annotations)

Shared: types/, util/, skill/, skillcheck/
DIAGRAM
```

```output
CLI (cmd/) → Orchestrate → Structure / Links / Content / Contamination
                         → Evaluate → Judge (LLM clients) → Cache
                         → Report (text / JSON / markdown / annotations)

Shared: types/, util/, skill/, skillcheck/
```

Let's start at the entry point.

---

## 1. Entry Point: `main.go` → `cmd.Execute()`

```bash
cat -n cmd/skill-validator/main.go
```

```output
     1	package main
     2	
     3	import (
     4		"github.com/agent-ecosystem/skill-validator/cmd"
     5	)
     6	
     7	func main() {
     8		cmd.Execute()
     9	}
```

Dead simple — delegates everything to the `cmd` package. That's where the Cobra CLI framework lives.

### The root command and version

```bash
sed -n "1,60p" cmd/root.go
```

```output
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/agent-ecosystem/skill-validator/skillcheck"
	"github.com/agent-ecosystem/skill-validator/types"
)

const version = "v1.2.0"

var (
	outputFormat    string
	emitAnnotations bool
)

var rootCmd = &cobra.Command{
	Use:   "skill-validator",
	Short: "Validate and analyze agent skills",
	Long:  "A CLI for validating skill directory structure, analyzing content quality, and detecting cross-language contamination.",
	// Once a command starts running (args parsed successfully), don't print
	// usage on error — the error is operational, not a CLI mistake.
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		cmd.SilenceUsage = true
	},
}

func init() {
	rootCmd.Version = version
	rootCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "text", "output format: text, json, or markdown")
	rootCmd.PersistentFlags().BoolVar(&emitAnnotations, "emit-annotations", false, "emit GitHub Actions workflow command annotations (::error/::warning) alongside normal output")
}

// Execute runs the root command.
func Execute() {
	// We handle error printing ourselves so that exitCodeError (validation
	// failures) doesn't produce cobra's default "Error: exit code N" noise.
	rootCmd.SilenceErrors = true
	if err := rootCmd.Execute(); err != nil {
		if ec, ok := err.(exitCodeError); ok {
			// Validation failure — report was already printed.
			os.Exit(ec.code)
		}
		// CLI/usage error — print and exit.
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(ExitCobra)
	}
}

// resolvePath resolves a path argument to an absolute directory path.
func resolvePath(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("path argument required")
	}

	dir := args[0]
```

Key design decisions visible here:

- **Exit code handling**: The tool uses a custom `exitCodeError` type to return structured exit codes (0=pass, 1=errors, 2=warnings, 3=CLI error) without Cobra printing its own error messages.
- **Output format**: Global `--output` flag supports text, JSON, and markdown.
- **Annotations**: `--emit-annotations` emits GitHub Actions `::error`/`::warning` commands for CI integration.

### Exit codes

```bash
cat -n cmd/exitcode.go
```

```output
     1	package cmd
     2	
     3	import "fmt"
     4	
     5	// Exit codes used by the CLI.
     6	const (
     7		ExitClean   = 0 // no errors, no warnings
     8		ExitError   = 1 // validation errors present
     9		ExitWarning = 2 // warnings present, no errors
    10		ExitCobra   = 3 // CLI/usage error (bad flags, missing args)
    11	)
    12	
    13	// exitCodeError is a sentinel error that carries a non-zero exit code.
    14	// It is returned by output helpers and handled by Execute().
    15	type exitCodeError struct {
    16		code int
    17	}
    18	
    19	func (e exitCodeError) Error() string {
    20		return fmt.Sprintf("exit code %d", e.code)
    21	}
    22	
    23	// exitOpts controls how validation results map to exit codes.
    24	type exitOpts struct {
    25		strict bool // when true, warnings are treated as errors (exit 1)
    26	}
    27	
    28	// resolve returns the appropriate exit code given error and warning counts.
    29	func (o exitOpts) resolve(errors, warnings int) int {
    30		if errors > 0 {
    31			return ExitError
    32		}
    33		if warnings > 0 {
    34			if o.strict {
    35				return ExitError
    36			}
    37			return ExitWarning
    38		}
    39		return ExitClean
    40	}
```

Clean design. The `--strict` flag promotes warnings to errors (exit 1), which is useful in CI pipelines where you want zero tolerance.

### The `check` command — the main entrypoint for users

```bash
cat -n cmd/check.go
```

```output
     1	package cmd
     2	
     3	import (
     4		"context"
     5		"fmt"
     6		"strings"
     7	
     8		"github.com/spf13/cobra"
     9	
    10		"github.com/agent-ecosystem/skill-validator/orchestrate"
    11		"github.com/agent-ecosystem/skill-validator/structure"
    12		"github.com/agent-ecosystem/skill-validator/types"
    13	)
    14	
    15	var (
    16		checkOnly        string
    17		checkSkip        string
    18		perFileCheck     bool
    19		checkSkipOrphans bool
    20		strictCheck      bool
    21	)
    22	
    23	var checkCmd = &cobra.Command{
    24		Use:   "check <path>",
    25		Short: "Run all checks (structure + links + content + contamination)",
    26		Long:  "Runs all validation and analysis checks. Use --only or --skip to select specific check groups.",
    27		Args:  cobra.ExactArgs(1),
    28		RunE:  runCheck,
    29	}
    30	
    31	func init() {
    32		checkCmd.Flags().StringVar(&checkOnly, "only", "", "comma-separated list of check groups to run: structure,links,content,contamination")
    33		checkCmd.Flags().StringVar(&checkSkip, "skip", "", "comma-separated list of check groups to skip: structure,links,content,contamination")
    34		checkCmd.Flags().BoolVar(&perFileCheck, "per-file", false, "show per-file reference analysis")
    35		checkCmd.Flags().BoolVar(&checkSkipOrphans, "skip-orphans", false,
    36			"skip orphan file detection (unreferenced files in scripts/, references/, assets/)")
    37		checkCmd.Flags().BoolVar(&strictCheck, "strict", false, "treat warnings as errors (exit 1 instead of 2)")
    38		rootCmd.AddCommand(checkCmd)
    39	}
    40	
    41	var validGroups = map[orchestrate.CheckGroup]bool{
    42		orchestrate.GroupStructure:     true,
    43		orchestrate.GroupLinks:         true,
    44		orchestrate.GroupContent:       true,
    45		orchestrate.GroupContamination: true,
    46	}
    47	
    48	func runCheck(cmd *cobra.Command, args []string) error {
    49		if checkOnly != "" && checkSkip != "" {
    50			return fmt.Errorf("--only and --skip are mutually exclusive")
    51		}
    52	
    53		enabled, err := resolveCheckGroups(checkOnly, checkSkip)
    54		if err != nil {
    55			return err
    56		}
    57	
    58		_, mode, dirs, err := detectAndResolve(args)
    59		if err != nil {
    60			return err
    61		}
    62	
    63		opts := orchestrate.Options{
    64			Enabled:    enabled,
    65			StructOpts: structure.Options{SkipOrphans: checkSkipOrphans},
    66		}
    67		eopts := exitOpts{strict: strictCheck}
    68		ctx := context.Background()
    69	
    70		switch mode {
    71		case types.SingleSkill:
    72			r := orchestrate.RunAllChecks(ctx, dirs[0], opts)
    73			return outputReportWithExitOpts(r, perFileCheck, eopts)
    74		case types.MultiSkill:
    75			mr := &types.MultiReport{}
    76			for _, dir := range dirs {
    77				r := orchestrate.RunAllChecks(ctx, dir, opts)
    78				mr.Skills = append(mr.Skills, r)
    79				mr.Errors += r.Errors
    80				mr.Warnings += r.Warnings
    81			}
    82			return outputMultiReportWithExitOpts(mr, perFileCheck, eopts)
    83		}
    84		return nil
    85	}
    86	
    87	func resolveCheckGroups(only, skip string) (map[orchestrate.CheckGroup]bool, error) {
    88		enabled := orchestrate.AllGroups()
    89	
    90		if only != "" {
    91			// Reset all to false, enable only specified
    92			for k := range enabled {
    93				enabled[k] = false
    94			}
    95			for g := range strings.SplitSeq(only, ",") {
    96				g = strings.TrimSpace(g)
    97				cg := orchestrate.CheckGroup(g)
    98				if !validGroups[cg] {
    99					return nil, fmt.Errorf("unknown check group %q (valid: structure, links, content, contamination)", g)
   100				}
   101				enabled[cg] = true
   102			}
   103		}
   104	
   105		if skip != "" {
   106			for g := range strings.SplitSeq(skip, ",") {
   107				g = strings.TrimSpace(g)
   108				cg := orchestrate.CheckGroup(g)
   109				if !validGroups[cg] {
   110					return nil, fmt.Errorf("unknown check group %q (valid: structure, links, content, contamination)", g)
   111				}
   112				enabled[cg] = false
   113			}
   114		}
   115	
   116		return enabled, nil
   117	}
```

The `check` command is the primary entrypoint. It:

1. Detects whether the path is a single skill or multi-skill parent
2. Builds an `orchestrate.Options` with enabled check groups
3. Delegates to `orchestrate.RunAllChecks()` for each skill directory
4. Outputs results and returns the appropriate exit code

The `--only`/`--skip` flags let users select check groups (structure, links, content, contamination). There are also dedicated subcommands (`validate structure`, `validate links`, `analyze content`, `analyze contamination`, `score evaluate`, `score report`) for running individual check groups.

---

## 2. Skill Detection: Single vs. Multi-Skill

Before any validation runs, the tool needs to figure out what it's looking at.

```bash
sed -n "1,54p" skillcheck/validator.go
```

```output
// Package skillcheck provides skill detection and reference analysis
// operations. Type definitions (Level, Result, Report, etc.) live in
// the types package.
package skillcheck

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/agent-ecosystem/skill-validator/contamination"
	"github.com/agent-ecosystem/skill-validator/content"
	"github.com/agent-ecosystem/skill-validator/types"
	"github.com/agent-ecosystem/skill-validator/util"
)

// DetectSkills determines whether dir is a single skill, a multi-skill
// parent, or contains no skills. It follows symlinks when checking
// subdirectories.
func DetectSkills(dir string) (types.SkillMode, []string) {
	// If the directory itself contains SKILL.md, it's a single skill.
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil {
		return types.SingleSkill, []string{dir}
	}

	// Scan immediate subdirectories for SKILL.md.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return types.NoSkill, nil
	}

	var skillDirs []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		subdir := filepath.Join(dir, name)
		// Use os.Stat (not entry.IsDir()) to follow symlinks.
		info, err := os.Stat(subdir)
		if err != nil || !info.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(subdir, "SKILL.md")); err == nil {
			skillDirs = append(skillDirs, subdir)
		}
	}

	if len(skillDirs) > 0 {
		return types.MultiSkill, skillDirs
	}
	return types.NoSkill, nil
}
```

Three modes:

| Mode | Condition | Example |
|------|-----------|---------|
| `SingleSkill` | Directory itself contains `SKILL.md` | `my-skill/SKILL.md` |
| `MultiSkill` | Subdirectories contain `SKILL.md` | `skills/foo/SKILL.md`, `skills/bar/SKILL.md` |
| `NoSkill` | No `SKILL.md` found | Error |

**Concern**: Line 43 uses `os.Stat()` which follows symlinks. A malicious skill package could symlink a subdirectory to `/etc/` or cloud metadata paths. This is tracked in [issue #3](https://github.com/x3c3/skill-validator/issues/3).

---

## 3. Skill Parsing: Frontmatter + Body

Once we know where the skill is, we parse its `SKILL.md`.

```bash
cat -n skill/skill.go
```

```output
     1	// Package skill handles parsing of SKILL.md files, including YAML frontmatter
     2	// extraction and body separation. It provides the core [Skill] type used by
     3	// validation and scoring packages.
     4	package skill
     5	
     6	import (
     7		"fmt"
     8		"os"
     9		"path/filepath"
    10		"strings"
    11	
    12		"gopkg.in/yaml.v3"
    13	)
    14	
    15	var _ yaml.Unmarshaler = (*AllowedTools)(nil)
    16	
    17	// Frontmatter represents the parsed YAML frontmatter of a SKILL.md file.
    18	type Frontmatter struct {
    19		Name          string            `yaml:"name"`
    20		Description   string            `yaml:"description"`
    21		License       string            `yaml:"license"`
    22		Compatibility string            `yaml:"compatibility"`
    23		Metadata      map[string]string `yaml:"metadata"`
    24		AllowedTools  AllowedTools      `yaml:"allowed-tools"`
    25	}
    26	
    27	// AllowedTools handles the type ambiguity in the allowed-tools field.
    28	// The spec defines it as a space-delimited string, but many skills use
    29	// a YAML list instead. This type accepts both.
    30	type AllowedTools struct {
    31		Value   string // normalized space-delimited string
    32		WasList bool   // true if the original YAML used a sequence
    33	}
    34	
    35	// UnmarshalYAML implements custom unmarshaling for AllowedTools to accept
    36	// both string and list formats.
    37	func (a *AllowedTools) UnmarshalYAML(value *yaml.Node) error {
    38		switch value.Kind {
    39		case yaml.ScalarNode:
    40			a.Value = value.Value
    41			a.WasList = false
    42			return nil
    43		case yaml.SequenceNode:
    44			var items []string
    45			if err := value.Decode(&items); err != nil {
    46				return fmt.Errorf("decoding allowed-tools list: %w", err)
    47			}
    48			a.Value = strings.Join(items, " ")
    49			a.WasList = true
    50			return nil
    51		default:
    52			return fmt.Errorf("allowed-tools must be a string or list, got YAML node kind %d", value.Kind)
    53		}
    54	}
    55	
    56	// IsEmpty returns true if no allowed-tools value was specified.
    57	func (a AllowedTools) IsEmpty() bool {
    58		return a.Value == ""
    59	}
    60	
    61	// Skill represents a parsed skill package.
    62	type Skill struct {
    63		Dir            string
    64		Frontmatter    Frontmatter
    65		RawFrontmatter map[string]any
    66		Body           string
    67		RawContent     string
    68	}
    69	
    70	// knownFrontmatterFields lists the frontmatter field names defined by the
    71	// skill spec. Fields not in this set trigger an "unrecognized field" warning.
    72	var knownFrontmatterFields = map[string]bool{
    73		"name":          true,
    74		"description":   true,
    75		"license":       true,
    76		"compatibility": true,
    77		"metadata":      true,
    78		"allowed-tools": true,
    79	}
    80	
    81	// Load reads and parses a SKILL.md file from the given directory.
    82	func Load(dir string) (*Skill, error) {
    83		path := filepath.Join(dir, "SKILL.md")
    84		data, err := os.ReadFile(path)
    85		if err != nil {
    86			return nil, fmt.Errorf("reading SKILL.md: %w", err)
    87		}
    88	
    89		content := string(data)
    90		skill := &Skill{
    91			Dir:        dir,
    92			RawContent: content,
    93		}
    94	
    95		fm, body, err := splitFrontmatter(content)
    96		if err != nil {
    97			return nil, err
    98		}
    99	
   100		skill.Body = body
   101	
   102		if fm != "" {
   103			if err := yaml.Unmarshal([]byte(fm), &skill.Frontmatter); err != nil {
   104				return nil, fmt.Errorf("parsing frontmatter YAML: %w", err)
   105			}
   106			if err := yaml.Unmarshal([]byte(fm), &skill.RawFrontmatter); err != nil {
   107				return nil, fmt.Errorf("parsing raw frontmatter: %w", err)
   108			}
   109		}
   110	
   111		return skill, nil
   112	}
   113	
   114	// UnrecognizedFields returns frontmatter field names not in the spec.
   115	func (s *Skill) UnrecognizedFields() []string {
   116		var unknown []string
   117		for k := range s.RawFrontmatter {
   118			if !knownFrontmatterFields[k] {
   119				unknown = append(unknown, k)
   120			}
   121		}
   122		return unknown
   123	}
   124	
   125	// splitFrontmatter separates YAML frontmatter (between --- delimiters) from the body.
   126	func splitFrontmatter(content string) (frontmatter, body string, err error) {
   127		if !strings.HasPrefix(content, "---") {
   128			return "", content, nil
   129		}
   130	
   131		// Find the closing ---
   132		rest := content[3:]
   133		// Skip the newline after opening ---
   134		if len(rest) > 0 && rest[0] == '\n' {
   135			rest = rest[1:]
   136		} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
   137			rest = rest[2:]
   138		}
   139	
   140		// Handle empty frontmatter (closing --- immediately)
   141		if strings.HasPrefix(rest, "---") {
   142			frontmatter = ""
   143			body = rest[3:]
   144			if len(body) > 0 && body[0] == '\n' {
   145				body = body[1:]
   146			} else if len(body) > 1 && body[0] == '\r' && body[1] == '\n' {
   147				body = body[2:]
   148			}
   149			return frontmatter, body, nil
   150		}
   151	
   152		before, after, ok := strings.Cut(rest, "\n---")
   153		if !ok {
   154			return "", "", fmt.Errorf("unterminated frontmatter: missing closing ---")
   155		}
   156	
   157		frontmatter = strings.TrimRight(before, "\r")
   158		body = after // skip \n---
   159		// Strip leading newline from body
   160		if len(body) > 0 && body[0] == '\n' {
   161			body = body[1:]
   162		} else if len(body) > 1 && body[0] == '\r' && body[1] == '\n' {
   163			body = body[2:]
   164		}
   165	
   166		return frontmatter, body, nil
   167	}
```

Notable details:

- **Dual parse**: The YAML is parsed twice — once into the typed `Frontmatter` struct, once into `map[string]any` (`RawFrontmatter`). The raw version enables detection of unrecognized fields without the struct silently dropping them.
- **AllowedTools ambiguity**: The spec says `allowed-tools` is a space-delimited string, but many real skills use YAML lists. The custom `UnmarshalYAML` handles both, tracking which format was used via `WasList`.
- **`splitFrontmatter`**: Hand-rolled parser for `---` delimiters with Windows CRLF handling. It uses `strings.Cut` for the split — clean and correct.
- **No file size limit**: `os.ReadFile` at line 84 reads the entire file. A multi-gigabyte file would OOM the process. Tracked in [issue #4](https://github.com/x3c3/skill-validator/issues/4).

---

## 4. Core Types: Results, Reports, and Levels

Before we look at validation, we need to understand the result types everything produces.

```bash
sed -n "1,80p" types/types.go
```

```output
// Package types defines the core data types used throughout the
// skill-validator: validation results, severity levels, token counts,
// skill modes, and aggregated reports.
package types

// Level represents the severity of a validation result.
type Level int

const (
	// Pass indicates a check passed successfully.
	Pass Level = iota
	// Info indicates an informational finding that requires no action.
	Info
	// Warning indicates a non-blocking issue that should be reviewed.
	Warning
	// Error indicates a blocking issue that must be fixed.
	Error
)

// String returns the lowercase name of the level.
func (l Level) String() string {
	switch l {
	case Pass:
		return "pass"
	case Info:
		return "info"
	case Warning:
		return "warning"
	case Error:
		return "error"
	default:
		return "unknown"
	}
}

// Result represents a single validation finding.
type Result struct {
	Level    Level
	Category string
	Message  string
	File     string // path relative to skill dir, e.g. "SKILL.md", "references/guide.md"
	Line     int    // 0 = no line info
}

// TokenCount holds the token count for a single file.
type TokenCount struct {
	File   string
	Tokens int
}

// ContentReport holds content quality metrics computed by the content analyzer.
type ContentReport struct {
	WordCount              int      `json:"word_count"`
	CodeBlockCount         int      `json:"code_block_count"`
	CodeBlockRatio         float64  `json:"code_block_ratio"`
	CodeLanguages          []string `json:"code_languages"`
	SentenceCount          int      `json:"sentence_count"`
	ImperativeCount        int      `json:"imperative_count"`
	ImperativeRatio        float64  `json:"imperative_ratio"`
	InformationDensity     float64  `json:"information_density"`
	StrongMarkers          int      `json:"strong_markers"`
	WeakMarkers            int      `json:"weak_markers"`
	InstructionSpecificity float64  `json:"instruction_specificity"`
	SectionCount           int      `json:"section_count"`
	ListItemCount          int      `json:"list_item_count"`
}

// ContaminationReport holds cross-language contamination metrics.
type ContaminationReport struct {
	MultiInterfaceTools  []string           `json:"multi_interface_tools"`
	CodeLanguages        []string           `json:"code_languages"`
	LanguageCategories   []string           `json:"language_categories"`
	PrimaryCategory      string             `json:"primary_category"`
	MismatchedCategories []string           `json:"mismatched_categories"`
	MismatchWeights      map[string]float64 `json:"mismatch_weights"`
	LanguageMismatch     bool               `json:"language_mismatch"`
	TechReferences       []string           `json:"tech_references"`
	ScopeBreadth         int                `json:"scope_breadth"`
	ContaminationScore   float64            `json:"contamination_score"`
	ContaminationLevel   string             `json:"contamination_level"`
```

```bash
sed -n "80,150p" types/types.go
```

```output
	ContaminationLevel   string             `json:"contamination_level"`
}

// ReferenceFileReport holds per-file content and contamination analysis for a single reference file.
type ReferenceFileReport struct {
	File                string
	ContentReport       *ContentReport
	ContaminationReport *ContaminationReport
}

// Report holds all validation results and token counts.
type Report struct {
	SkillDir                      string
	Results                       []Result
	TokenCounts                   []TokenCount
	OtherTokenCounts              []TokenCount
	ContentReport                 *ContentReport
	ReferencesContentReport       *ContentReport
	ContaminationReport           *ContaminationReport
	ReferencesContaminationReport *ContaminationReport
	ReferenceReports              []ReferenceFileReport
	Errors                        int
	Warnings                      int
}

// Tally counts errors and warnings in the report.
func (r *Report) Tally() {
	r.Errors = 0
	r.Warnings = 0
	for _, result := range r.Results {
		switch result.Level {
		case Error:
			r.Errors++
		case Warning:
			r.Warnings++
		}
	}
}

// SkillMode indicates what kind of skill directory was detected.
type SkillMode int

const (
	// NoSkill means no SKILL.md was found in the directory.
	NoSkill SkillMode = iota
	// SingleSkill means the directory itself contains a SKILL.md.
	SingleSkill
	// MultiSkill means the directory contains subdirectories with SKILL.md files.
	MultiSkill
)

// MultiReport holds aggregated results from validating multiple skills.
type MultiReport struct {
	Skills   []*Report
	Errors   int
	Warnings int
}

// DimensionScore holds a single scoring dimension's display name and value.
type DimensionScore struct {
	Label string // Display name, e.g., "Token Efficiency"
	Value int    // Score value, typically 1-5
}

// Scored is the interface implemented by both SkillScores and RefScores.
// It allows formatting code to iterate dimensions generically.
type Scored interface {
	DimensionScores() []DimensionScore
	OverallScore() float64
	Assessment() string
	NovelDetails() string
```

The `Result` struct is the universal currency — every check produces a slice of `Result`s. The `Report` aggregates them along with token counts and analysis reports. `Tally()` counts errors and warnings for exit code resolution.

### The ResultContext builder

Creating results with consistent category/file info is simplified by `ResultContext`:

```bash
cat -n types/context.go
```

```output
     1	package types
     2	
     3	import "fmt"
     4	
     5	// ResultContext is a builder that sets Category and a default File once,
     6	// so individual results inherit location context automatically.
     7	type ResultContext struct {
     8		Category string
     9		File     string // default file; methods like ErrorFile override it
    10	}
    11	
    12	func (c ResultContext) result(level Level, file string, line int, msg string) Result {
    13		if file == "" {
    14			file = c.File
    15		}
    16		return Result{
    17			Level:    level,
    18			Category: c.Category,
    19			Message:  msg,
    20			File:     file,
    21			Line:     line,
    22		}
    23	}
    24	
    25	// Pass creates a pass result using the default file.
    26	func (c ResultContext) Pass(msg string) Result { return c.result(Pass, "", 0, msg) }
    27	
    28	// Passf creates a formatted pass result using the default file.
    29	func (c ResultContext) Passf(format string, args ...any) Result {
    30		return c.result(Pass, "", 0, fmt.Sprintf(format, args...))
    31	}
    32	
    33	// Info creates an info result using the default file.
    34	func (c ResultContext) Info(msg string) Result { return c.result(Info, "", 0, msg) }
    35	
    36	// Infof creates a formatted info result using the default file.
    37	func (c ResultContext) Infof(format string, args ...any) Result {
    38		return c.result(Info, "", 0, fmt.Sprintf(format, args...))
    39	}
    40	
    41	// Warn creates a warning result using the default file.
    42	func (c ResultContext) Warn(msg string) Result { return c.result(Warning, "", 0, msg) }
    43	
    44	// Warnf creates a formatted warning result using the default file.
    45	func (c ResultContext) Warnf(format string, args ...any) Result {
    46		return c.result(Warning, "", 0, fmt.Sprintf(format, args...))
    47	}
    48	
    49	// Error creates an error result using the default file.
    50	func (c ResultContext) Error(msg string) Result { return c.result(Error, "", 0, msg) }
    51	
    52	// Errorf creates a formatted error result using the default file.
    53	func (c ResultContext) Errorf(format string, args ...any) Result {
    54		return c.result(Error, "", 0, fmt.Sprintf(format, args...))
    55	}
    56	
    57	// PassFile creates a pass result with an explicit file.
    58	func (c ResultContext) PassFile(file, msg string) Result { return c.result(Pass, file, 0, msg) }
    59	
    60	// WarnFile creates a warning result with an explicit file.
    61	func (c ResultContext) WarnFile(file, msg string) Result { return c.result(Warning, file, 0, msg) }
    62	
    63	// WarnFilef creates a formatted warning result with an explicit file.
    64	func (c ResultContext) WarnFilef(file, format string, args ...any) Result {
    65		return c.result(Warning, file, 0, fmt.Sprintf(format, args...))
    66	}
    67	
    68	// ErrorFile creates an error result with an explicit file.
    69	func (c ResultContext) ErrorFile(file, msg string) Result { return c.result(Error, file, 0, msg) }
    70	
    71	// ErrorFilef creates a formatted error result with an explicit file.
    72	func (c ResultContext) ErrorFilef(file, format string, args ...any) Result {
    73		return c.result(Error, file, 0, fmt.Sprintf(format, args...))
    74	}
    75	
    76	// ErrorAtLine creates an error result with an explicit file and line number.
    77	func (c ResultContext) ErrorAtLine(file string, line int, msg string) Result {
    78		return c.result(Error, file, line, msg)
    79	}
    80	
    81	// ErrorAtLinef creates a formatted error result with an explicit file and line number.
    82	func (c ResultContext) ErrorAtLinef(file string, line int, format string, args ...any) Result {
    83		return c.result(Error, file, line, fmt.Sprintf(format, args...))
    84	}
```

This is a clean builder pattern. You set `Category` and `File` once, then call `.Pass()`, `.Warn()`, `.Error()`, etc. The `*File` and `*AtLine` variants override defaults for specific findings. It eliminates repeated boilerplate across all check functions.

---

## 5. Structure Validation: The Main Pipeline

The `structure` package is the largest and most important. Let's trace the validation pipeline.

```bash
cat -n structure/validate.go
```

```output
     1	// Package structure validates the directory layout, frontmatter, token counts,
     2	// markdown syntax, internal links, and orphan files of a skill package. It is
     3	// the main validation entry point used by the CLI.
     4	package structure
     5	
     6	import (
     7		"github.com/agent-ecosystem/skill-validator/skill"
     8		"github.com/agent-ecosystem/skill-validator/types"
     9		"github.com/agent-ecosystem/skill-validator/util"
    10	)
    11	
    12	// Options configures which checks Validate runs.
    13	type Options struct {
    14		SkipOrphans bool
    15	}
    16	
    17	// ValidateMulti validates each directory and returns an aggregated report.
    18	func ValidateMulti(dirs []string, opts Options) *types.MultiReport {
    19		mr := &types.MultiReport{}
    20		for _, dir := range dirs {
    21			r := Validate(dir, opts)
    22			mr.Skills = append(mr.Skills, r)
    23			mr.Errors += r.Errors
    24			mr.Warnings += r.Warnings
    25		}
    26		return mr
    27	}
    28	
    29	// Validate runs all checks against the skill in the given directory.
    30	func Validate(dir string, opts Options) *types.Report {
    31		report := &types.Report{SkillDir: dir}
    32	
    33		// Structure checks
    34		structResults := CheckStructure(dir)
    35		report.Results = append(report.Results, structResults...)
    36	
    37		// Check if SKILL.md was found; if not, skip further checks
    38		hasSkillMD := false
    39		for _, r := range structResults {
    40			if r.Level == types.Pass && r.Message == "SKILL.md found" {
    41				hasSkillMD = true
    42				break
    43			}
    44		}
    45		if !hasSkillMD {
    46			report.Tally()
    47			return report
    48		}
    49	
    50		// Parse skill
    51		s, err := skill.Load(dir)
    52		if err != nil {
    53			report.Results = append(report.Results,
    54				types.ResultContext{Category: "Frontmatter", File: "SKILL.md"}.Error(err.Error()))
    55			report.Tally()
    56			return report
    57		}
    58	
    59		// Frontmatter checks
    60		report.Results = append(report.Results, CheckFrontmatter(s)...)
    61	
    62		// Token checks
    63		tokenResults, tokenCounts, otherCounts := CheckTokens(dir, s.Body)
    64		report.Results = append(report.Results, tokenResults...)
    65		report.TokenCounts = tokenCounts
    66		report.OtherTokenCounts = otherCounts
    67	
    68		// Holistic structure check: is this actually a skill?
    69		report.Results = append(report.Results, checkSkillRatio(report.TokenCounts, report.OtherTokenCounts)...)
    70	
    71		// Markdown structure checks (unclosed code fences)
    72		report.Results = append(report.Results, CheckMarkdown(dir, s.Body)...)
    73	
    74		// Internal link checks (broken relative links are a structural issue)
    75		report.Results = append(report.Results, CheckInternalLinks(dir, s.Body)...)
    76	
    77		// Orphan file checks (files in recognized dirs that are never referenced)
    78		if !opts.SkipOrphans {
    79			report.Results = append(report.Results, CheckOrphanFiles(dir, s.Body)...)
    80		}
    81	
    82		report.Tally()
    83		return report
    84	}
    85	
    86	func checkSkillRatio(standard, other []types.TokenCount) []types.Result {
    87		ctx := types.ResultContext{Category: "Overall"}
    88		standardTotal := 0
    89		for _, tc := range standard {
    90			standardTotal += tc.Tokens
    91		}
    92		otherTotal := 0
    93		for _, tc := range other {
    94			otherTotal += tc.Tokens
    95		}
    96	
    97		if otherTotal > 25_000 && standardTotal > 0 && otherTotal > standardTotal*10 {
    98			return []types.Result{ctx.Errorf(
    99				"this content doesn't appear to be structured as a skill — "+
   100					"there are %s tokens of non-standard content but only %s tokens in the "+
   101					"standard skill structure (SKILL.md + references). This ratio suggests a "+
   102					"build pipeline issue or content that belongs in a different format, not a skill. "+
   103					"Per the spec, a skill should contain a focused SKILL.md with optional references, "+
   104					"scripts, and assets.",
   105				util.FormatNumber(otherTotal), util.FormatNumber(standardTotal),
   106			)}
   107		}
   108	
   109		return nil
   110	}
```

The pipeline is sequential with early exit:

1. **CheckStructure** — directory layout (SKILL.md exists? known dirs? extraneous files?)
2. **skill.Load** — parse frontmatter + body (early exit on parse failure)
3. **CheckFrontmatter** — required fields, unrecognized fields, allowed-tools format
4. **CheckTokens** — count tokens per file using o200k_base encoding
5. **checkSkillRatio** — holistic check: is non-standard content >10x the standard content?
6. **CheckMarkdown** — unclosed code fences
7. **CheckInternalLinks** — broken relative links within the skill
8. **CheckOrphanFiles** — files in `scripts/`, `references/`, `assets/` never referenced

Let's look at each check in turn.

### 5a. Directory Structure Checks

```bash
cat -n structure/checks.go
```

```output
     1	package structure
     2	
     3	import (
     4		"fmt"
     5		"os"
     6		"path/filepath"
     7		"strings"
     8	
     9		"github.com/agent-ecosystem/skill-validator/types"
    10		"github.com/agent-ecosystem/skill-validator/util"
    11	)
    12	
    13	// recognizedDirs lists the directory names defined by the skill spec.
    14	var recognizedDirs = map[string]bool{
    15		"scripts":    true,
    16		"references": true,
    17		"assets":     true,
    18	}
    19	
    20	// Files commonly found in repos but not intended for agent consumption.
    21	// Per Anthropic best practices: "A skill should only contain essential files
    22	// that directly support its functionality."
    23	// See: github.com/anthropics/skills → skill-creator
    24	var knownExtraneousFiles = map[string]string{
    25		"readme.md":             "README.md",
    26		"readme":                "README",
    27		"changelog.md":          "CHANGELOG.md",
    28		"changelog":             "CHANGELOG",
    29		"license":               "LICENSE",
    30		"license.md":            "LICENSE.md",
    31		"license.txt":           "LICENSE.txt",
    32		"contributing.md":       "CONTRIBUTING.md",
    33		"code_of_conduct.md":    "CODE_OF_CONDUCT.md",
    34		"installation_guide.md": "INSTALLATION_GUIDE.md",
    35		"quick_reference.md":    "QUICK_REFERENCE.md",
    36		"makefile":              "Makefile",
    37		".gitignore":            ".gitignore",
    38	}
    39	
    40	// CheckStructure validates the directory layout of a skill package. It checks
    41	// for the required SKILL.md file, flags unrecognized directories and extraneous
    42	// root files, and warns about deep nesting in recognized directories.
    43	func CheckStructure(dir string) []types.Result {
    44		ctx := types.ResultContext{Category: "Structure"}
    45		var results []types.Result
    46	
    47		// Check SKILL.md exists
    48		skillPath := filepath.Join(dir, "SKILL.md")
    49		if _, err := os.Stat(skillPath); os.IsNotExist(err) {
    50			results = append(results, ctx.ErrorFile("SKILL.md", "SKILL.md not found"))
    51			return results
    52		}
    53		results = append(results, ctx.PassFile("SKILL.md", "SKILL.md found"))
    54	
    55		// Check directories
    56		entries, err := os.ReadDir(dir)
    57		if err != nil {
    58			results = append(results, ctx.Errorf("reading directory: %v", err))
    59			return results
    60		}
    61	
    62		for _, entry := range entries {
    63			name := entry.Name()
    64			if strings.HasPrefix(name, ".") {
    65				continue // skip hidden files/dirs
    66			}
    67			if !entry.IsDir() {
    68				if name != "SKILL.md" {
    69					results = append(results, extraneousFileResult(ctx, name))
    70				}
    71				continue
    72			}
    73			if !recognizedDirs[name] {
    74				msg := fmt.Sprintf("unknown directory: %s/", name)
    75				if subEntries, err := os.ReadDir(filepath.Join(dir, name)); err == nil {
    76					fileCount := 0
    77					for _, se := range subEntries {
    78						if !strings.HasPrefix(se.Name(), ".") {
    79							fileCount++
    80						}
    81					}
    82					if fileCount > 0 {
    83						hint := unknownDirHint(dir)
    84						msg = fmt.Sprintf(
    85							"unknown directory: %s/ (contains %d file%s) — agents using the standard skill structure won't discover these files%s",
    86							name, fileCount, util.PluralS(fileCount), hint,
    87						)
    88					}
    89				}
    90				results = append(results, ctx.Warn(msg))
    91			}
    92		}
    93	
    94		// Check for deep nesting in recognized directories
    95		for dirName := range recognizedDirs {
    96			subdir := filepath.Join(dir, dirName)
    97			if _, err := os.Stat(subdir); os.IsNotExist(err) {
    98				continue
    99			}
   100			err := checkNesting(ctx, subdir, dirName)
   101			if err != nil {
   102				results = append(results, err...)
   103			}
   104		}
   105	
   106		return results
   107	}
   108	
   109	func extraneousFileResult(ctx types.ResultContext, name string) types.Result {
   110		lower := strings.ToLower(name)
   111		if lower == "agents.md" {
   112			return ctx.WarnFile(name, fmt.Sprintf(
   113				"%s is for repo-level agent configuration, not skill content — "+
   114					"move it outside the skill directory (e.g. to the repository root) "+
   115					"where agents discover it automatically",
   116				name,
   117			))
   118		}
   119		if _, known := knownExtraneousFiles[lower]; known {
   120			return ctx.WarnFile(name, fmt.Sprintf(
   121				"%s is not needed in a skill — agents may load it into their context window, "+
   122					"taking space from your actual task (Anthropic best practices: skills should only "+
   123					"contain files that directly support agent functionality)",
   124				name,
   125			))
   126		}
   127		return ctx.WarnFile(name, fmt.Sprintf(
   128			"unexpected file at root: %s — if agents need this file, move it into "+
   129				"references/ or assets/ as appropriate; otherwise remove it to avoid "+
   130				"unnecessary context window usage",
   131			name,
   132		))
   133	}
   134	
   135	func unknownDirHint(dir string) string {
   136		var candidates []string
   137		if _, err := os.Stat(filepath.Join(dir, "references")); os.IsNotExist(err) {
   138			candidates = append(candidates, "references/")
   139		}
   140		if _, err := os.Stat(filepath.Join(dir, "assets")); os.IsNotExist(err) {
   141			candidates = append(candidates, "assets/")
   142		}
   143		if len(candidates) == 0 {
   144			return ""
   145		}
   146		return fmt.Sprintf("; should this be %s?", strings.Join(candidates, " or "))
   147	}
   148	
   149	func checkNesting(ctx types.ResultContext, dir, prefix string) []types.Result {
   150		var results []types.Result
   151		entries, err := os.ReadDir(dir)
   152		if err != nil {
   153			return results
   154		}
   155		for _, entry := range entries {
   156			if strings.HasPrefix(entry.Name(), ".") {
   157				continue
   158			}
   159			if entry.IsDir() {
   160				results = append(results, ctx.Warnf("deep nesting detected: %s/%s/", prefix, entry.Name()))
   161			}
   162		}
   163		return results
   164	}
```

The structure checker is thoughtful about its error messages. Unknown directories get a hint suggesting where files should go ("should this be `references/`?"). Extraneous files get specific guidance about why they waste context window space.

The `agents.md` case is interesting — it's a repo-level file that agents look for in the repository root, not inside a skill directory.

### 5b. Frontmatter Validation

```bash
cat -n structure/frontmatter.go
```

```output
     1	package structure
     2	
     3	import (
     4		"path/filepath"
     5		"regexp"
     6		"strings"
     7	
     8		"github.com/agent-ecosystem/skill-validator/skill"
     9		"github.com/agent-ecosystem/skill-validator/types"
    10	)
    11	
    12	var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
    13	
    14	// CheckFrontmatter validates the YAML frontmatter of a parsed skill. It checks
    15	// required fields (name, description), enforces format and length constraints,
    16	// validates optional fields, and warns about unrecognized or keyword-stuffed fields.
    17	func CheckFrontmatter(s *skill.Skill) []types.Result {
    18		ctx := types.ResultContext{Category: "Frontmatter", File: "SKILL.md"}
    19		var results []types.Result
    20	
    21		// Check name
    22		name := s.Frontmatter.Name
    23		if name == "" {
    24			results = append(results, ctx.Error("name is required"))
    25		} else {
    26			if len(name) > 64 {
    27				results = append(results, ctx.Errorf("name exceeds 64 characters (%d)", len(name)))
    28			}
    29			if !namePattern.MatchString(name) {
    30				results = append(results, ctx.Errorf("name %q must be lowercase alphanumeric with hyphens, no leading/trailing/consecutive hyphens", name))
    31			}
    32			// Check that name matches directory name
    33			dirName := filepath.Base(s.Dir)
    34			if name != dirName {
    35				results = append(results, ctx.Errorf("name does not match directory name (expected %q, got %q)", dirName, name))
    36			}
    37			if len(results) == 0 || (name != "" && namePattern.MatchString(name)) {
    38				results = append(results, ctx.Passf("name: %q (valid)", name))
    39			}
    40		}
    41	
    42		// Check description
    43		desc := s.Frontmatter.Description
    44		if desc == "" {
    45			results = append(results, ctx.Error("description is required"))
    46		} else if len(desc) > 1024 {
    47			results = append(results, ctx.Errorf("description exceeds 1024 characters (%d)", len(desc)))
    48		} else if strings.TrimSpace(desc) == "" {
    49			results = append(results, ctx.Error("description must not be empty/whitespace-only"))
    50		} else {
    51			results = append(results, ctx.Passf("description: (%d chars)", len(desc)))
    52			results = append(results, checkDescriptionKeywordStuffing(ctx, desc)...)
    53		}
    54	
    55		// Check optional license
    56		if s.Frontmatter.License != "" {
    57			results = append(results, ctx.Passf("license: %q", s.Frontmatter.License))
    58		}
    59	
    60		// Check optional compatibility
    61		if s.Frontmatter.Compatibility != "" {
    62			if len(s.Frontmatter.Compatibility) > 500 {
    63				results = append(results, ctx.Errorf("compatibility exceeds 500 characters (%d)", len(s.Frontmatter.Compatibility)))
    64			} else {
    65				results = append(results, ctx.Passf("compatibility: (%d chars)", len(s.Frontmatter.Compatibility)))
    66			}
    67		}
    68	
    69		// Check optional metadata
    70		if s.RawFrontmatter["metadata"] != nil {
    71			// Verify it's a map[string]string
    72			if m, ok := s.RawFrontmatter["metadata"].(map[string]any); ok {
    73				allStrings := true
    74				for k, v := range m {
    75					if _, ok := v.(string); !ok {
    76						results = append(results, ctx.Errorf("metadata[%q] value must be a string", k))
    77						allStrings = false
    78					}
    79				}
    80				if allStrings {
    81					results = append(results, ctx.Passf("metadata: (%d entries)", len(m)))
    82				}
    83			} else {
    84				results = append(results, ctx.Error("metadata must be a map of string keys to string values"))
    85			}
    86		}
    87	
    88		// Check optional allowed-tools
    89		if !s.Frontmatter.AllowedTools.IsEmpty() {
    90			results = append(results, ctx.Passf("allowed-tools: %q", s.Frontmatter.AllowedTools.Value))
    91			if s.Frontmatter.AllowedTools.WasList {
    92				results = append(results, ctx.Info("allowed-tools is a YAML list; the spec defines this as a space-delimited string — both are accepted, but a string is more portable across agent implementations"))
    93			}
    94		}
    95	
    96		// Warn on unrecognized fields
    97		for _, field := range s.UnrecognizedFields() {
    98			results = append(results, ctx.Warnf("unrecognized field: %q", field))
    99		}
   100	
   101		return results
   102	}
   103	
   104	var quotedStringPattern = regexp.MustCompile(`"[^"]*"`)
   105	
   106	func checkDescriptionKeywordStuffing(ctx types.ResultContext, desc string) []types.Result {
   107		// Heuristic 1: Many quoted strings with insufficient prose context suggest keyword stuffing.
   108		// Descriptions that have substantial prose alongside quoted trigger lists are fine —
   109		// the spec encourages keywords, and many good descriptions use a prose sentence
   110		// followed by a supplementary trigger list.
   111		quotes := quotedStringPattern.FindAllString(desc, -1)
   112		if len(quotes) >= 5 {
   113			// Strip all quoted strings to measure the remaining prose
   114			prose := quotedStringPattern.ReplaceAllString(desc, "")
   115			proseWordCount := 0
   116			for w := range strings.FieldsSeq(prose) {
   117				// Skip punctuation-only tokens (commas, periods, colons, etc.)
   118				cleaned := strings.TrimFunc(w, func(r rune) bool {
   119					return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
   120				})
   121				if len(cleaned) > 0 {
   122					proseWordCount++
   123				}
   124			}
   125			// If the prose (outside quotes) has fewer words than quoted strings,
   126			// the description is dominated by keyword lists
   127			if proseWordCount < len(quotes) {
   128				return []types.Result{ctx.Warnf(
   129					"description contains %d quoted strings with little surrounding prose — "+
   130						"this looks like keyword stuffing; per the spec, the description should "+
   131						"concisely describe what the skill does and when to use it, not just list trigger phrases",
   132					len(quotes),
   133				)}
   134			}
   135		}
   136	
   137		// Heuristic 2: Many comma-separated short segments suggest a bare keyword list.
   138		// Strip quoted strings first so that prose + trigger-list descriptions aren't penalized.
   139		descWithoutQuotes := quotedStringPattern.ReplaceAllString(desc, "")
   140		allSegments := strings.Split(descWithoutQuotes, ",")
   141		var segments []string
   142		for _, seg := range allSegments {
   143			if strings.TrimSpace(seg) != "" {
   144				segments = append(segments, seg)
   145			}
   146		}
   147		if len(segments) >= 8 {
   148			shortCount := 0
   149			for _, seg := range segments {
   150				words := strings.Fields(strings.TrimSpace(seg))
   151				if len(words) <= 3 {
   152					shortCount++
   153				}
   154			}
   155			if shortCount*100/len(segments) >= 60 {
   156				return []types.Result{ctx.Warnf(
   157					"description has %d comma-separated segments, most very short — "+
   158						"this looks like a keyword list; per the spec, the description should "+
   159						"concisely describe what the skill does and when to use it",
   160					len(segments),
   161				)}
   162			}
   163		}
   164	
   165		return nil
   166	}
```

Frontmatter validation enforces:

- **name**: Required, ≤64 chars, lowercase-alphanumeric-with-hyphens, must match directory name
- **description**: Required, ≤1024 chars, non-whitespace-only, with keyword-stuffing detection
- **compatibility**: Optional, ≤500 chars
- **metadata**: Optional, must be `map[string]string`
- **allowed-tools**: Optional, warns if YAML list format is used instead of spec's string format

The keyword-stuffing detector is clever — two heuristics catch different patterns:
1. Many quoted strings with little prose context
2. Many short comma-separated segments (bare keyword lists)

Both strip quoted strings before measuring, so legitimate "prose + trigger list" descriptions aren't penalized.

### 5c. Token Counting

```bash
cat -n structure/tokens.go
```

```output
     1	package structure
     2	
     3	import (
     4		"os"
     5		"path/filepath"
     6		"strings"
     7	
     8		"github.com/agent-ecosystem/skill-validator/types"
     9		"github.com/tiktoken-go/tokenizer"
    10	)
    11	
    12	const (
    13		// refFileSoftLimit is the per-file token warning threshold for reference files.
    14		refFileSoftLimit = 10_000
    15		// refFileHardLimit is the per-file token error threshold for reference files.
    16		refFileHardLimit = 25_000
    17	
    18		// refTotalSoftLimit is the aggregate token warning threshold across all reference files.
    19		refTotalSoftLimit = 25_000
    20		// refTotalHardLimit is the aggregate token error threshold across all reference files.
    21		refTotalHardLimit = 50_000
    22	
    23		// otherTotalSoftLimit is the aggregate token warning threshold for non-standard files.
    24		otherTotalSoftLimit = 25_000
    25		// otherTotalHardLimit is the aggregate token error threshold for non-standard files.
    26		otherTotalHardLimit = 100_000
    27	)
    28	
    29	// CheckTokens counts tokens for the SKILL.md body, reference files, asset files,
    30	// and non-standard files. It returns validation results, standard token counts,
    31	// and non-standard ("other") token counts.
    32	func CheckTokens(dir, body string) ([]types.Result, []types.TokenCount, []types.TokenCount) {
    33		ctx := types.ResultContext{Category: "Tokens"}
    34		var results []types.Result
    35		var counts []types.TokenCount
    36	
    37		enc, err := tokenizer.Get(tokenizer.O200kBase)
    38		if err != nil {
    39			results = append(results, ctx.Errorf("failed to initialize tokenizer: %v", err))
    40			return results, counts, nil
    41		}
    42	
    43		// Count SKILL.md body tokens
    44		bodyTokens, _, _ := enc.Encode(body)
    45		bodyCount := len(bodyTokens)
    46		counts = append(counts, types.TokenCount{File: "SKILL.md body", Tokens: bodyCount})
    47	
    48		// Warn if body exceeds 5000 tokens
    49		if bodyCount > 5000 {
    50			results = append(results, ctx.WarnFilef("SKILL.md", "SKILL.md body is %d tokens (spec recommends < 5000)", bodyCount))
    51		}
    52	
    53		// Warn if SKILL.md exceeds 500 lines
    54		lineCount := strings.Count(body, "\n") + 1
    55		if lineCount > 500 {
    56			results = append(results, ctx.WarnFilef("SKILL.md", "SKILL.md body is %d lines (spec recommends < 500)", lineCount))
    57		}
    58	
    59		// Count tokens for files in references/
    60		refTotal := 0
    61		refsDir := filepath.Join(dir, "references")
    62		if entries, err := os.ReadDir(refsDir); err == nil {
    63			for _, entry := range entries {
    64				if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
    65					continue
    66				}
    67				path := filepath.Join(refsDir, entry.Name())
    68				data, err := os.ReadFile(path)
    69				if err != nil {
    70					relPath := filepath.Join("references", entry.Name())
    71					results = append(results, ctx.WarnFilef(relPath, "could not read %s: %v", relPath, err))
    72					continue
    73				}
    74				tokens, _, _ := enc.Encode(string(data))
    75				fileTokens := len(tokens)
    76				relPath := filepath.Join("references", entry.Name())
    77				counts = append(counts, types.TokenCount{
    78					File:   relPath,
    79					Tokens: fileTokens,
    80				})
    81				refTotal += fileTokens
    82	
    83				// Per-file limits
    84				if fileTokens > refFileHardLimit {
    85					results = append(results, ctx.ErrorFilef(relPath,
    86						"%s is %d tokens — this will consume 12-20%% of a typical context window "+
    87							"and meaningfully degrade agent performance; split into smaller focused files",
    88						relPath, fileTokens,
    89					))
    90				} else if fileTokens > refFileSoftLimit {
    91					results = append(results, ctx.WarnFilef(relPath,
    92						"%s is %d tokens — consider splitting into smaller focused files "+
    93							"so agents load only what they need",
    94						relPath, fileTokens,
    95					))
    96				}
    97			}
    98		}
    99	
   100		// Aggregate reference limits
   101		if refTotal > refTotalHardLimit {
   102			results = append(results, ctx.Errorf(
   103				"total reference files: %d tokens — this will consume 25-40%% of a typical "+
   104					"context window; reduce content or split into a skill with fewer references",
   105				refTotal,
   106			))
   107		} else if refTotal > refTotalSoftLimit {
   108			results = append(results, ctx.Warnf(
   109				"total reference files: %d tokens — agents may load multiple references "+
   110					"in one session, consider whether all this content is essential",
   111				refTotal,
   112			))
   113		}
   114	
   115		// Count tokens in non-standard files
   116		otherCounts := countOtherFiles(dir, enc)
   117	
   118		// Check other-files aggregate limits
   119		otherTotal := 0
   120		for _, c := range otherCounts {
   121			otherTotal += c.Tokens
   122		}
   123		if otherTotal > otherTotalHardLimit {
   124			results = append(results, ctx.Errorf(
   125				"non-standard files total %d tokens — if an agent loads these, "+
   126					"they will consume most of the context window and severely degrade performance; "+
   127					"move essential content into references/ or remove unnecessary files",
   128				otherTotal,
   129			))
   130		} else if otherTotal > otherTotalSoftLimit {
   131			results = append(results, ctx.Warnf(
   132				"non-standard files total %d tokens — if an agent loads these, "+
   133					"they could consume a significant portion of the context window; "+
   134					"consider moving essential content into references/ or removing unnecessary files",
   135				otherTotal,
   136			))
   137		}
   138	
   139		// Count tokens in text-based asset files
   140		assetCounts := countAssetFiles(dir, enc)
   141		counts = append(counts, assetCounts...)
   142	
   143		return results, counts, otherCounts
   144	}
   145	
   146	// binaryExtensions lists file extensions that are skipped during token counting
   147	// because they are not text-based content.
   148	var binaryExtensions = map[string]bool{
   149		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true,
   150		".ico": true, ".svg": true, ".webp": true,
   151		".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
   152		".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".7z": true, ".rar": true,
   153		".exe": true, ".dll": true, ".so": true, ".dylib": true, ".bin": true,
   154		".mp3": true, ".mp4": true, ".wav": true, ".avi": true, ".mov": true,
   155		".woff": true, ".woff2": true, ".ttf": true, ".eot": true, ".otf": true,
   156	}
   157	
   158	// standardRootFiles lists root-level files already counted in the main token
   159	// table, so they are excluded from the "other files" count.
   160	var standardRootFiles = map[string]bool{
   161		"skill.md": true,
   162	}
   163	
   164	// standardDirs lists directories already handled by the standard skill
   165	// structure, so their contents are excluded from the "other files" count.
   166	var standardDirs = map[string]bool{
   167		"references": true,
   168		"scripts":    true,
   169		"assets":     true,
   170	}
   171	
   172	// textAssetExtensions lists file extensions in assets/ that are text-based and
   173	// likely loaded into LLM context (templates, guides, configs). These are
   174	// included in token counting.
   175	var textAssetExtensions = map[string]bool{
   176		".md":       true,
   177		".tex":      true,
   178		".py":       true,
   179		".yaml":     true,
   180		".yml":      true,
   181		".tsx":      true,
   182		".ts":       true,
   183		".jsx":      true,
   184		".sty":      true,
   185		".mplstyle": true,
   186		".ipynb":    true,
   187	}
   188	
   189	func countAssetFiles(dir string, enc tokenizer.Codec) []types.TokenCount {
   190		var counts []types.TokenCount
   191		assetsDir := filepath.Join(dir, "assets")
   192	
   193		_ = filepath.Walk(assetsDir, func(path string, info os.FileInfo, err error) error {
   194			if err != nil {
   195				return nil
   196			}
   197			if info.IsDir() {
   198				if strings.HasPrefix(info.Name(), ".") && path != assetsDir {
   199					return filepath.SkipDir
   200				}
   201				return nil
   202			}
   203			if strings.HasPrefix(info.Name(), ".") {
   204				return nil
   205			}
   206			ext := strings.ToLower(filepath.Ext(info.Name()))
   207			if !textAssetExtensions[ext] {
   208				return nil
   209			}
   210			data, err := os.ReadFile(path)
   211			if err != nil {
   212				return nil
   213			}
   214			rel, _ := filepath.Rel(dir, path)
   215			tokens, _, _ := enc.Encode(string(data))
   216			counts = append(counts, types.TokenCount{File: rel, Tokens: len(tokens)})
   217			return nil
   218		})
   219	
   220		return counts
   221	}
   222	
   223	func countOtherFiles(dir string, enc tokenizer.Codec) []types.TokenCount {
   224		var counts []types.TokenCount
   225	
   226		entries, err := os.ReadDir(dir)
   227		if err != nil {
   228			return counts
   229		}
   230	
   231		for _, entry := range entries {
   232			name := entry.Name()
   233			if strings.HasPrefix(name, ".") {
   234				continue
   235			}
   236	
   237			if entry.IsDir() {
   238				if standardDirs[strings.ToLower(name)] {
   239					continue
   240				}
   241				// Walk files in unknown directory
   242				counts = append(counts, countFilesInDir(dir, name, enc)...)
   243			} else {
   244				if standardRootFiles[strings.ToLower(name)] {
   245					continue
   246				}
   247				if binaryExtensions[strings.ToLower(filepath.Ext(name))] {
   248					continue
   249				}
   250				data, err := os.ReadFile(filepath.Join(dir, name))
   251				if err != nil {
   252					continue
   253				}
   254				tokens, _, _ := enc.Encode(string(data))
   255				counts = append(counts, types.TokenCount{File: name, Tokens: len(tokens)})
   256			}
   257		}
   258	
   259		return counts
   260	}
   261	
   262	func countFilesInDir(rootDir, dirName string, enc tokenizer.Codec) []types.TokenCount {
   263		var counts []types.TokenCount
   264		fullDir := filepath.Join(rootDir, dirName)
   265	
   266		_ = filepath.Walk(fullDir, func(path string, info os.FileInfo, err error) error {
   267			if err != nil {
   268				return nil
   269			}
   270			if info.IsDir() {
   271				if strings.HasPrefix(info.Name(), ".") && path != fullDir {
   272					return filepath.SkipDir
   273				}
   274				return nil
   275			}
   276			if strings.HasPrefix(info.Name(), ".") {
   277				return nil
   278			}
   279			if binaryExtensions[strings.ToLower(filepath.Ext(info.Name()))] {
   280				return nil
   281			}
   282			data, err := os.ReadFile(path)
   283			if err != nil {
   284				return nil
   285			}
   286			rel, _ := filepath.Rel(rootDir, path)
   287			tokens, _, _ := enc.Encode(string(data))
   288			counts = append(counts, types.TokenCount{File: rel, Tokens: len(tokens)})
   289			return nil
   290		})
   291	
   292		return counts
   293	}
```

Token counting uses the **o200k_base** tokenizer (GPT-4o/Claude's encoding). Three categories of files are counted:

| Category | What | Limits |
|----------|------|--------|
| SKILL.md body | The instruction content | Warn >5000 tokens, warn >500 lines |
| Reference files | `references/*.md` | Per-file: warn >10K, error >25K. Aggregate: warn >25K, error >50K |
| Other files | Everything outside standard dirs | Aggregate: warn >25K, error >100K |
| Text assets | Text files in `assets/` | Counted but no limits |

The limits are pragmatic — they're based on context window consumption. A 25K-token reference file consumes ~12-20% of a typical context window.

### 5d. Markdown Structure Checks

```bash
cat -n structure/markdown.go
```

````output
     1	package structure
     2	
     3	import (
     4		"os"
     5		"path/filepath"
     6		"strings"
     7	
     8		"github.com/agent-ecosystem/skill-validator/types"
     9	)
    10	
    11	// CheckMarkdown validates markdown structure in the skill.
    12	func CheckMarkdown(dir, body string) []types.Result {
    13		ctx := types.ResultContext{Category: "Markdown"}
    14		var results []types.Result
    15	
    16		// Check SKILL.md body
    17		if line, ok := FindUnclosedFence(body); ok {
    18			results = append(results, ctx.ErrorAtLinef("SKILL.md", line,
    19				"SKILL.md has an unclosed code fence starting at line %d — this may cause agents to misinterpret everything after it as code", line))
    20		}
    21	
    22		// Check .md files in references/
    23		refsDir := filepath.Join(dir, "references")
    24		entries, err := os.ReadDir(refsDir)
    25		if err != nil {
    26			if len(results) == 0 {
    27				results = append(results, ctx.Pass("no unclosed code fences found"))
    28			}
    29			return results
    30		}
    31		for _, entry := range entries {
    32			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
    33				continue
    34			}
    35			if !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
    36				continue
    37			}
    38			data, err := os.ReadFile(filepath.Join(refsDir, entry.Name()))
    39			if err != nil {
    40				continue
    41			}
    42			relPath := filepath.Join("references", entry.Name())
    43			if line, ok := FindUnclosedFence(string(data)); ok {
    44				results = append(results, ctx.ErrorAtLinef(relPath, line,
    45					"%s has an unclosed code fence starting at line %d — this may cause agents to misinterpret everything after it as code", relPath, line))
    46			}
    47		}
    48	
    49		if len(results) == 0 {
    50			results = append(results, ctx.Pass("no unclosed code fences found"))
    51		}
    52	
    53		return results
    54	}
    55	
    56	// FindUnclosedFence checks for unclosed code fences (``` or ~~~).
    57	// Returns the line number of the unclosed opening fence and true, or 0 and false.
    58	func FindUnclosedFence(content string) (int, bool) {
    59		lines := strings.Split(content, "\n")
    60		inFence := false
    61		fenceChar := byte(0)
    62		fenceLen := 0
    63		fenceLine := 0
    64	
    65		for i, line := range lines {
    66			// Strip up to 3 leading spaces
    67			stripped := line
    68			for range 3 {
    69				if len(stripped) > 0 && stripped[0] == ' ' {
    70					stripped = stripped[1:]
    71				} else {
    72					break
    73				}
    74			}
    75	
    76			if !inFence {
    77				if char, n := fencePrefix(stripped); n >= 3 {
    78					inFence = true
    79					fenceChar = char
    80					fenceLen = n
    81					fenceLine = i + 1
    82				}
    83			} else {
    84				if char, n := fencePrefix(stripped); n >= fenceLen && char == fenceChar {
    85					// Closing fence: rest must be only whitespace
    86					rest := stripped[n:]
    87					if strings.TrimSpace(rest) == "" {
    88						inFence = false
    89					}
    90				}
    91			}
    92		}
    93	
    94		if inFence {
    95			return fenceLine, true
    96		}
    97		return 0, false
    98	}
    99	
   100	// fencePrefix returns the fence character and its count if the line starts
   101	// with 3+ backticks or 3+ tildes. Returns (0, 0) otherwise.
   102	func fencePrefix(line string) (byte, int) {
   103		if len(line) == 0 {
   104			return 0, 0
   105		}
   106		ch := line[0]
   107		if ch != '`' && ch != '~' {
   108			return 0, 0
   109		}
   110		n := 0
   111		for n < len(line) && line[n] == ch {
   112			n++
   113		}
   114		if n < 3 {
   115			return 0, 0
   116		}
   117		return ch, n
   118	}
````

This detects unclosed code fences (triple backticks or tildes) — a common bug in skill files that causes agents to misinterpret everything after the fence as code. The implementation correctly handles:

- Up to 3 leading spaces (per CommonMark spec)
- Both backtick and tilde fences
- Closing fences must use the same character and be at least as long as the opening
- Closing fence rest-of-line must be whitespace only

The check runs on both SKILL.md and all reference markdown files.

### 5e. Internal Links

```bash
cat -n structure/links.go
```

```output
     1	package structure
     2	
     3	import (
     4		"os"
     5		"path/filepath"
     6		"strings"
     7	
     8		"github.com/agent-ecosystem/skill-validator/links"
     9		"github.com/agent-ecosystem/skill-validator/types"
    10	)
    11	
    12	// CheckInternalLinks validates relative (internal) links in the skill body.
    13	// Broken internal links indicate a structural problem: the skill references
    14	// files that don't exist in the package.
    15	func CheckInternalLinks(dir, body string) []types.Result {
    16		ctx := types.ResultContext{Category: "Structure", File: "SKILL.md"}
    17		allLinks := links.ExtractLinks(body)
    18		if len(allLinks) == 0 {
    19			return nil
    20		}
    21	
    22		var results []types.Result
    23	
    24		for _, link := range allLinks {
    25			// Skip template URLs containing {placeholder} variables (RFC 6570 URI Templates)
    26			if strings.Contains(link, "{") {
    27				continue
    28			}
    29			// Skip HTTP(S) links — those are external
    30			if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
    31				continue
    32			}
    33			// Skip mailto and anchor links
    34			if strings.HasPrefix(link, "mailto:") || strings.HasPrefix(link, "#") {
    35				continue
    36			}
    37			// Strip fragment identifier (e.g. "guide.md#heading" → "guide.md")
    38			link, _, _ = strings.Cut(link, "#")
    39			if link == "" {
    40				continue
    41			}
    42			// Relative link — check file existence
    43			resolved := filepath.Join(dir, link)
    44			if _, err := os.Stat(resolved); os.IsNotExist(err) {
    45				results = append(results, ctx.Errorf("broken internal link: %s (file not found)", link))
    46			} else {
    47				results = append(results, ctx.Passf("internal link: %s (exists)", link))
    48			}
    49		}
    50	
    51		return results
    52	}
```

Internal link checking reuses the same `links.ExtractLinks` function as external link checking. It filters to relative paths (not HTTP, mailto, or anchor links), strips fragment identifiers, and verifies the file exists on disk.

### 5f. Orphan File Detection

```bash
cat -n structure/orphans.go
```

```output
     1	package structure
     2	
     3	import (
     4		"fmt"
     5		"os"
     6		"path/filepath"
     7		"regexp"
     8		"strings"
     9	
    10		"github.com/agent-ecosystem/skill-validator/types"
    11	)
    12	
    13	// orderedRecognizedDirs lists the recognized subdirectories in a stable order
    14	// for deterministic output. These match the keys in recognizedDirs (checks.go).
    15	var orderedRecognizedDirs = []string{"assets", "references", "scripts"}
    16	
    17	// queueItem represents a text body to scan during the BFS reachability walk.
    18	type queueItem struct {
    19		text   string
    20		source string // file that provided this text ("SKILL.md" for the seed)
    21	}
    22	
    23	// CheckOrphanFiles walks scripts/, references/, and assets/ to find files
    24	// that are never referenced (directly or transitively) from SKILL.md.
    25	func CheckOrphanFiles(dir, body string) []types.Result {
    26		ctx := types.ResultContext{Category: "Structure"}
    27	
    28		// Inventory: collect all files in recognized directories.
    29		inventory := inventoryFiles(dir)
    30		if len(inventory) == 0 {
    31			return nil
    32		}
    33	
    34		// Collect root-level text files (excluding SKILL.md) that can serve as
    35		// intermediaries in the reference chain. These aren't in the inventory
    36		// (we don't check whether they're orphaned), but they can bridge
    37		// SKILL.md to files in recognized directories (e.g., FORMS.md, package.json).
    38		rootFiles := rootTextFiles(dir)
    39	
    40		// BFS reachability from SKILL.md body.
    41		reached := make(map[string]bool)          // relPath → true
    42		reachedFrom := make(map[string]string)    // relPath → parent that first referenced it ("SKILL.md" for direct)
    43		missingExtension := make(map[string]bool) // relPath → true if matched only without file extension
    44		scannedRootFiles := make(map[string]bool)
    45		scannedInitFiles := make(map[string]bool)
    46	
    47		// Seed the queue with the SKILL.md body.
    48		queue := []queueItem{{text: body, source: "SKILL.md"}}
    49	
    50		for len(queue) > 0 {
    51			item := queue[0]
    52			queue = queue[1:]
    53	
    54			// Determine the directory of the source file so we can resolve
    55			// relative paths. For SKILL.md (the seed), the base is the root.
    56			sourceDir := ""
    57			if item.source != "SKILL.md" {
    58				sourceDir = filepath.Dir(item.source)
    59			}
    60	
    61			// Check if the current text references any root-level files we
    62			// haven't scanned yet. If so, read and enqueue them as intermediaries.
    63			// Use case-insensitive matching since skill authors commonly use
    64			// different casing (e.g., "FORMS.md" in text, "forms.md" on disk).
    65			lowerText := strings.ToLower(item.text)
    66			for _, rf := range rootFiles {
    67				if scannedRootFiles[rf] {
    68					continue
    69				}
    70				if strings.Contains(lowerText, strings.ToLower(rf)) {
    71					scannedRootFiles[rf] = true
    72					data, err := os.ReadFile(filepath.Join(dir, rf))
    73					if err == nil {
    74						queue = append(queue, queueItem{text: string(data), source: rf})
    75					}
    76				}
    77			}
    78	
    79			isPython := strings.HasSuffix(item.source, ".py")
    80	
    81			for _, relPath := range inventory {
    82				if reached[relPath] {
    83					continue
    84				}
    85				if containsReference(item.text, sourceDir, relPath) {
    86					markReached(relPath, item.source, dir, &queue, reached, reachedFrom, inventory)
    87				} else if isPython && pythonImportReaches(item.text, item.source, relPath) {
    88					// Python import resolution takes priority over the extensionless
    89					// fallback so that normal import statements (e.g., "from helpers
    90					// import merge") don't trigger a "missing extension" warning.
    91					markReached(relPath, item.source, dir, &queue, reached, reachedFrom, inventory)
    92				} else if containsReferenceWithoutExtension(item.text, sourceDir, relPath) {
    93					markReached(relPath, item.source, dir, &queue, reached, reachedFrom, inventory)
    94					missingExtension[relPath] = true
    95				}
    96			}
    97	
    98			// For Python files, check if any imports resolve to package directories
    99			// (i.e., directories with __init__.py). The __init__.py files are excluded
   100			// from inventory so they don't get orphan warnings, but they can act as
   101			// bridges: e.g., pack.py does "from validators import X" which hits
   102			// validators/__init__.py, which re-exports from .base, .docx, etc.
   103			if isPython {
   104				for _, initPath := range pythonPackageInits(item.text, item.source, dir) {
   105					if scannedInitFiles[initPath] {
   106						continue
   107					}
   108					scannedInitFiles[initPath] = true
   109					data, err := os.ReadFile(filepath.Join(dir, initPath))
   110					if err == nil {
   111						queue = append(queue, queueItem{text: string(data), source: initPath})
   112					}
   113				}
   114			}
   115		}
   116	
   117		// Build results per directory.
   118		var results []types.Result
   119	
   120		for _, d := range orderedRecognizedDirs {
   121			dirFiles := filesInDir(inventory, d)
   122			if len(dirFiles) == 0 {
   123				continue
   124			}
   125	
   126			hasOrphans := false
   127			for _, relPath := range dirFiles {
   128				if !reached[relPath] {
   129					hasOrphans = true
   130					results = append(results, ctx.WarnFile(relPath,
   131						fmt.Sprintf("potentially unreferenced file: %s — agents may not discover this file without an explicit reference in SKILL.md or a referenced file", relPath)))
   132				} else if missingExtension[relPath] {
   133					ext := filepath.Ext(relPath)
   134					noExt := strings.TrimSuffix(relPath, ext)
   135					results = append(results, ctx.WarnFile(relPath,
   136						fmt.Sprintf("file %s is referenced without its extension (as %s in %s) — include the %s extension so agents can reliably locate the file", relPath, noExt, reachedFrom[relPath], ext)))
   137				}
   138			}
   139	
   140			if !hasOrphans {
   141				results = append(results, ctx.Passf("all files in %s/ are referenced", d))
   142			}
   143		}
   144	
   145		return results
   146	}
   147	
   148	// rootTextFiles returns the names of text files in the skill root directory,
   149	// excluding SKILL.md. These files aren't tracked as inventory (we don't warn
   150	// about them being orphaned), but they participate in the BFS as intermediaries
   151	// that can bridge SKILL.md to files in recognized directories.
   152	func rootTextFiles(dir string) []string {
   153		entries, err := os.ReadDir(dir)
   154		if err != nil {
   155			return nil
   156		}
   157		var files []string
   158		for _, entry := range entries {
   159			if entry.IsDir() {
   160				continue
   161			}
   162			name := entry.Name()
   163			if strings.EqualFold(name, "SKILL.md") {
   164				continue
   165			}
   166			if isTextFile(name) {
   167				files = append(files, name)
   168			}
   169		}
   170		return files
   171	}
   172	
   173	// inventoryFiles collects relative paths for all files under recognized directories.
   174	func inventoryFiles(dir string) []string {
   175		var files []string
   176		for _, d := range orderedRecognizedDirs {
   177			subdir := filepath.Join(dir, d)
   178			err := filepath.WalkDir(subdir, func(path string, entry os.DirEntry, err error) error {
   179				if err != nil {
   180					return nil // skip inaccessible paths
   181				}
   182				if entry.IsDir() {
   183					return nil
   184				}
   185				// Skip __init__.py files — these are Python package markers that
   186				// are never referenced by name. Warning about them is pure noise:
   187				// if siblings are reached they're implicitly needed, and if the
   188				// whole directory is orphaned the other files will be flagged.
   189				if entry.Name() == "__init__.py" {
   190					return nil
   191				}
   192				rel, _ := filepath.Rel(dir, path)
   193				files = append(files, rel)
   194				return nil
   195			})
   196			if err != nil {
   197				continue
   198			}
   199		}
   200		return files
   201	}
   202	
   203	// filesInDir returns inventory entries that start with the given directory prefix.
   204	func filesInDir(inventory []string, dir string) []string {
   205		prefix := dir + string(filepath.Separator)
   206		var out []string
   207		for _, f := range inventory {
   208			if strings.HasPrefix(f, prefix) {
   209				out = append(out, f)
   210			}
   211		}
   212		return out
   213	}
   214	
   215	// containsReference checks whether text references relPath, either by its full
   216	// root-relative path or by a path relative to sourceDir. For example, a file at
   217	// references/guide.md might reference images/diagram.png, which should match the
   218	// inventory entry references/images/diagram.png.
   219	func containsReference(text, sourceDir, relPath string) bool {
   220		// Direct match: the full root-relative path appears in the text.
   221		if strings.Contains(text, relPath) {
   222			return true
   223		}
   224		// Relative match: if the source is in a subdirectory, check whether the
   225		// path relative to that directory appears in the text.
   226		if sourceDir != "" {
   227			rel, err := filepath.Rel(sourceDir, relPath)
   228			if err == nil && !strings.HasPrefix(rel, "..") && strings.Contains(text, rel) {
   229				return true
   230			}
   231		}
   232		return false
   233	}
   234	
   235	// containsReferenceWithoutExtension is like containsReference but strips the
   236	// file extension before matching. This catches cases where skill authors
   237	// reference scripts without the extension (e.g., "scripts/check_fillable_fields"
   238	// instead of "scripts/check_fillable_fields.py").
   239	func containsReferenceWithoutExtension(text, sourceDir, relPath string) bool {
   240		ext := filepath.Ext(relPath)
   241		if ext == "" {
   242			return false
   243		}
   244		noExt := strings.TrimSuffix(relPath, ext)
   245		return containsReference(text, sourceDir, noExt)
   246	}
   247	
   248	// markReached marks a file as reached, reads it if it's a text file, and
   249	// enqueues its content for further BFS scanning.
   250	func markReached(relPath, source, dir string, queue *[]queueItem, reached map[string]bool, reachedFrom map[string]string, inventory []string) {
   251		reached[relPath] = true
   252		reachedFrom[relPath] = source
   253	
   254		if isTextFile(relPath) {
   255			data, err := os.ReadFile(filepath.Join(dir, relPath))
   256			if err == nil {
   257				*queue = append(*queue, queueItem{text: string(data), source: relPath})
   258			}
   259		}
   260	}
   261	
   262	// pythonImportRe matches Python import statements:
   263	//   - "from module import ..."
   264	//   - "from .module import ..."
   265	//   - "from ..module import ..."
   266	//   - "import module"
   267	var pythonImportRe = regexp.MustCompile(`(?m)^\s*(?:from\s+(\.{0,2}[\w.]+)\s+import|import\s+([\w.]+))`)
   268	
   269	// pythonImportReaches checks whether a Python source file's import statements
   270	// resolve to the given inventory path. Module paths like "helpers.merge_runs"
   271	// are converted to file paths ("helpers/merge_runs.py") and resolved relative
   272	// to the importing file's directory.
   273	func pythonImportReaches(text, source, relPath string) bool {
   274		if !strings.HasSuffix(relPath, ".py") {
   275			return false
   276		}
   277		sourceDir := filepath.Dir(source)
   278	
   279		for _, match := range pythonImportRe.FindAllStringSubmatch(text, -1) {
   280			// match[1] is the "from X import" module, match[2] is the "import X" module
   281			module := match[1]
   282			if module == "" {
   283				module = match[2]
   284			}
   285	
   286			// Handle relative imports: the first dot means "current package"
   287			// (same directory as the importing file). Each additional dot goes
   288			// one level up (.. = parent package, ... = grandparent, etc.).
   289			resolveDir := sourceDir
   290			if strings.HasPrefix(module, ".") {
   291				module = module[1:] // first dot: current package (no directory change)
   292				for strings.HasPrefix(module, ".") {
   293					module = module[1:]
   294					resolveDir = filepath.Dir(resolveDir)
   295				}
   296			}
   297			if module == "" {
   298				continue
   299			}
   300	
   301			// Convert dotted module path to file path: helpers.merge_runs → helpers/merge_runs
   302			modulePath := strings.ReplaceAll(module, ".", string(filepath.Separator))
   303	
   304			// Try resolving as a .py file relative to the source directory.
   305			candidate := filepath.Join(resolveDir, modulePath+".py")
   306			if candidate == relPath {
   307				return true
   308			}
   309		}
   310		return false
   311	}
   312	
   313	// pythonPackageInits returns relative paths to __init__.py files for any
   314	// Python imports in text that resolve to package directories rather than .py
   315	// files. For example, "from validators import X" in scripts/office/pack.py
   316	// resolves to scripts/office/validators/__init__.py if that file exists on disk.
   317	func pythonPackageInits(text, source, dir string) []string {
   318		sourceDir := filepath.Dir(source)
   319		var inits []string
   320	
   321		for _, match := range pythonImportRe.FindAllStringSubmatch(text, -1) {
   322			module := match[1]
   323			if module == "" {
   324				module = match[2]
   325			}
   326	
   327			resolveDir := sourceDir
   328			if strings.HasPrefix(module, ".") {
   329				module = module[1:]
   330				for strings.HasPrefix(module, ".") {
   331					module = module[1:]
   332					resolveDir = filepath.Dir(resolveDir)
   333				}
   334			}
   335			if module == "" {
   336				continue
   337			}
   338	
   339			modulePath := strings.ReplaceAll(module, ".", string(filepath.Separator))
   340			initPath := filepath.Join(resolveDir, modulePath, "__init__.py")
   341	
   342			// Check if the __init__.py actually exists on disk.
   343			if _, err := os.Stat(filepath.Join(dir, initPath)); err == nil {
   344				inits = append(inits, initPath)
   345			}
   346		}
   347		return inits
   348	}
   349	
   350	// isTextFile checks whether the file extension indicates a scannable text file.
   351	// Anything not in the binary extension list is assumed to be text.
   352	func isTextFile(relPath string) bool {
   353		return !binaryExtensions[strings.ToLower(filepath.Ext(relPath))]
   354	}
```

This is the most sophisticated check. It performs **BFS reachability analysis** starting from SKILL.md:

1. **Inventory** all files under `scripts/`, `references/`, `assets/`
2. **Seed** the BFS queue with the SKILL.md body
3. For each queued text:
   - Check if it references any inventory file (by path substring)
   - If a text file is reached, read it and enqueue for further scanning
   - Root-level files act as intermediaries (bridges from SKILL.md to subdirectory files)
4. **Python-specific**: Resolve `import` and `from...import` statements to file paths, including relative imports (`.module`, `..module`) and `__init__.py` package markers
5. **Extension-less references**: Catch `scripts/check_fields` → `scripts/check_fields.py` with a warning to include the extension

Unreferenced files get a warning — agents won't discover them without explicit references.

---

## 6. External Link Validation

The `links` package handles HTTP/HTTPS link checking.

### Link Extraction

```bash
cat -n links/extract.go
```

````output
     1	// Package links extracts and validates hyperlinks found in skill markdown
     2	// content. It handles both markdown-style links and bare URLs, checks HTTP
     3	// links concurrently, and reports broken or unreachable URLs.
     4	package links
     5	
     6	import (
     7		"regexp"
     8		"strings"
     9	)
    10	
    11	var (
    12		// mdLinkPattern matches [text](url) markdown links.
    13		mdLinkPattern = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
    14		// bareURLPattern matches bare URLs starting with http:// or https://.
    15		bareURLPattern = regexp.MustCompile("(?:^|\\s)(https?://[^\\s<>\\)`]+)")
    16		// codeBlockStrip removes fenced code blocks before link extraction.
    17		codeBlockStrip = regexp.MustCompile("(?s)(?:```|~~~)[\\w]*\\n.*?(?:```|~~~)")
    18		// inlineCodeStrip removes inline code spans before link extraction.
    19		inlineCodeStrip = regexp.MustCompile("`[^`]+`")
    20	)
    21	
    22	// ExtractLinks extracts all unique links from a markdown body.
    23	func ExtractLinks(body string) []string {
    24		seen := make(map[string]bool)
    25		var links []string
    26	
    27		// Strip code fences and inline code spans so URLs in code are not extracted.
    28		cleaned := codeBlockStrip.ReplaceAllString(body, "")
    29		cleaned = inlineCodeStrip.ReplaceAllString(cleaned, "")
    30	
    31		// Markdown links
    32		for _, match := range mdLinkPattern.FindAllStringSubmatch(cleaned, -1) {
    33			url := strings.TrimSpace(match[2])
    34			if !seen[url] {
    35				seen[url] = true
    36				links = append(links, url)
    37			}
    38		}
    39	
    40		// Bare URLs
    41		for _, match := range bareURLPattern.FindAllStringSubmatch(cleaned, -1) {
    42			url := trimTrailingDelimiters(strings.TrimSpace(match[1]))
    43			if !seen[url] {
    44				seen[url] = true
    45				links = append(links, url)
    46			}
    47		}
    48	
    49		return links
    50	}
    51	
    52	var entitySuffix = regexp.MustCompile(`&[a-zA-Z0-9]+;$`)
    53	
    54	// trimTrailingDelimiters strips trailing punctuation and entity references
    55	// from bare URLs, following cmark-gfm's autolink delimiter rules.
    56	func trimTrailingDelimiters(url string) string {
    57		for {
    58			changed := false
    59	
    60			// Strip trailing HTML entity references (e.g. &amp;)
    61			if strings.HasSuffix(url, ";") {
    62				if loc := entitySuffix.FindStringIndex(url); loc != nil {
    63					url = url[:loc[0]]
    64					changed = true
    65					continue
    66				}
    67			}
    68	
    69			// Strip unbalanced trailing closing parenthesis
    70			if strings.HasSuffix(url, ")") {
    71				open := strings.Count(url, "(")
    72				close := strings.Count(url, ")")
    73				if close > open {
    74					url = url[:len(url)-1]
    75					changed = true
    76					continue
    77				}
    78			}
    79	
    80			// Strip trailing punctuation
    81			if len(url) > 0 && strings.ContainsRune("?!.,:*_~'\";<", rune(url[len(url)-1])) {
    82				url = url[:len(url)-1]
    83				changed = true
    84				continue
    85			}
    86	
    87			if !changed {
    88				break
    89			}
    90		}
    91		return url
    92	}
````

Link extraction is careful:

1. **Strip code blocks** first — URLs inside fenced code or inline code aren't real links
2. Extract **markdown links** `[text](url)` and **bare URLs** `https://...`
3. **Deduplicate** via `seen` map
4. **Trim trailing delimiters** following cmark-gfm autolink rules — handles balanced parens, HTML entities, trailing punctuation

### Link Checking (concurrent HEAD requests)

We already saw `links/check.go` in the security review. The key design:

- Shared `http.Client` for connection reuse
- Goroutine per link with `sync.WaitGroup`
- 10s timeout, max 10 redirects
- HEAD requests with a custom User-Agent
- HTTP 403 treated as info (many sites block automated requests) rather than error

**Concern**: `checkHTTPLink` doesn't use the context passed to `CheckLinks`, so cancellation doesn't propagate to in-flight requests. Tracked in [issue #2](https://github.com/x3c3/skill-validator/issues/2).

---

## 7. Content Analysis

The `content` package computes quality metrics on skill text.

```bash
sed -n "1,100p" content/content.go
```

````output
// Package content analyzes the textual content of SKILL.md files. It computes
// metrics such as word count, code block ratio, imperative sentence ratio,
// information density, and instruction specificity to assess content quality.
package content

import (
	"regexp"
	"strings"

	"github.com/agent-ecosystem/skill-validator/types"
	"github.com/agent-ecosystem/skill-validator/util"
)

// strongMarkerRes contains pre-compiled patterns for strong directive language
// markers (must, always, never, etc.) used to measure instruction specificity.
var strongMarkerRes = compilePatterns([]string{
	`\bmust\b`, `\balways\b`, `\bnever\b`, `\bshall\b`,
	`\brequired\b`, `\bdo not\b`, `\bdon't\b`, `\bensure\b`,
	`\bcritical\b`, `\bmandatory\b`,
})

// weakMarkerRes contains pre-compiled patterns for weak/advisory language
// markers (may, consider, could, etc.) used to measure instruction specificity.
var weakMarkerRes = compilePatterns([]string{
	`\bmay\b`, `\bconsider\b`, `\bcould\b`, `\bmight\b`,
	`\boptional\b`, `\bpossibly\b`, `\bsuggested\b`,
	`\bprefer\b`, `\btry to\b`, `\bif possible\b`,
})

func compilePatterns(patterns []string) []*regexp.Regexp {
	res := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		res[i] = regexp.MustCompile(p)
	}
	return res
}

// imperativeVerbs is the set of common imperative verbs used to detect
// instruction-style sentences in skill content.
var imperativeVerbs = map[string]bool{
	"use": true, "run": true, "create": true, "add": true, "set": true,
	"install": true, "configure": true, "write": true, "read": true,
	"check": true, "verify": true, "make": true, "build": true, "test": true,
	"ensure": true, "include": true, "remove": true, "delete": true,
	"update": true, "call": true, "import": true, "export": true,
	"define": true, "implement": true, "return": true, "pass": true,
	"handle": true, "parse": true, "generate": true, "format": true,
	"validate": true, "convert": true, "follow": true, "apply": true,
	"start": true, "stop": true, "avoid": true, "keep": true, "do": true,
	"execute": true, "open": true, "close": true, "save": true, "load": true,
	"send": true, "receive": true,
}

var (
	codeBlockPattern = regexp.MustCompile("(?s)```[\\w]*\\n(.*?)```")
	codeLangPattern  = regexp.MustCompile("```(\\w+)")
	codeBlockStrip   = regexp.MustCompile("(?s)```[\\w]*\\n.*?```")
	inlineCodeStrip  = regexp.MustCompile("`[^`]+`")
	sentenceSplitPat = regexp.MustCompile(`[.!?]\s+|[.!?]$|\n\n+`)
	leadingFormatPat = regexp.MustCompile(`^[#*\->\s]+`)
	sectionPattern   = regexp.MustCompile(`(?m)^#{2,}\s+`)
	listItemPattern  = regexp.MustCompile(`(?m)^[\s]*[-*+]\s+|^\s*\d+\.\s+`)
)

// Analyze computes content metrics for SKILL.md content.
func Analyze(content string) *types.ContentReport {
	if strings.TrimSpace(content) == "" {
		return &types.ContentReport{}
	}

	words := strings.Fields(content)
	wordCount := len(words)

	// Code block analysis
	codeBlocks := codeBlockPattern.FindAllStringSubmatch(content, -1)
	codeBlockCount := len(codeBlocks)
	codeBlockWords := 0
	for _, match := range codeBlocks {
		codeBlockWords += len(strings.Fields(match[1]))
	}
	codeBlockRatio := 0.0
	if wordCount > 0 {
		codeBlockRatio = float64(codeBlockWords) / float64(wordCount)
	}

	// Code languages
	langMatches := codeLangPattern.FindAllStringSubmatch(content, -1)
	codeLanguages := make([]string, 0, len(langMatches))
	for _, m := range langMatches {
		codeLanguages = append(codeLanguages, m[1])
	}

	// Sentence analysis
	sentences := countSentences(content)
	sentenceCount := len(sentences)
	imperativeCount := countImperativeSentences(sentences)
	imperativeRatio := 0.0
	if sentenceCount > 0 {
		imperativeRatio = float64(imperativeCount) / float64(sentenceCount)
	}
````

```bash
sed -n "100,180p" content/content.go
```

```output
	}

	// Information density: when code blocks are present, factor in the
	// code-to-prose ratio; otherwise score purely on imperative ratio so
	// prose-only skills aren't penalized for lacking code.
	informationDensity := imperativeRatio
	if codeBlockCount > 0 {
		informationDensity = (codeBlockRatio * 0.5) + (imperativeRatio * 0.5)
	}

	// Language marker analysis
	strongCount := countMarkerMatches(content, strongMarkerRes)
	weakCount := countMarkerMatches(content, weakMarkerRes)
	totalMarkers := strongCount + weakCount
	instructionSpecificity := 0.0
	if totalMarkers > 0 {
		instructionSpecificity = float64(strongCount) / float64(totalMarkers)
	}

	// Section count (H2+ headers)
	sectionCount := len(sectionPattern.FindAllString(content, -1))

	// List item count
	listItemCount := len(listItemPattern.FindAllString(content, -1))

	return &types.ContentReport{
		WordCount:              wordCount,
		CodeBlockCount:         codeBlockCount,
		CodeBlockRatio:         util.RoundTo(codeBlockRatio, 4),
		CodeLanguages:          codeLanguages,
		SentenceCount:          sentenceCount,
		ImperativeCount:        imperativeCount,
		ImperativeRatio:        util.RoundTo(imperativeRatio, 4),
		InformationDensity:     util.RoundTo(informationDensity, 4),
		StrongMarkers:          strongCount,
		WeakMarkers:            weakCount,
		InstructionSpecificity: util.RoundTo(instructionSpecificity, 4),
		SectionCount:           sectionCount,
		ListItemCount:          listItemCount,
	}
}

func countSentences(text string) []string {
	// Remove code blocks first
	text = codeBlockStrip.ReplaceAllString(text, "")
	// Remove inline code
	text = inlineCodeStrip.ReplaceAllString(text, "")
	// Split on sentence boundaries
	parts := sentenceSplitPat.Split(text, -1)
	var sentences []string
	for _, s := range parts {
		s = strings.TrimSpace(s)
		if s != "" && len(s) > 5 {
			sentences = append(sentences, s)
		}
	}
	return sentences
}

func countImperativeSentences(sentences []string) int {
	count := 0
	for _, sentence := range sentences {
		// Get first word, stripping markdown formatting
		cleaned := leadingFormatPat.ReplaceAllString(sentence, "")
		words := strings.Fields(cleaned)
		if len(words) == 0 {
			continue
		}
		firstWord := strings.ToLower(words[0])
		if imperativeVerbs[firstWord] {
			count++
		}
	}
	return count
}

func countMarkerMatches(text string, patterns []*regexp.Regexp) int {
	total := 0
	textLower := strings.ToLower(text)
	for _, re := range patterns {
		total += len(re.FindAllString(textLower, -1))
```

Content analysis produces a `ContentReport` with these metrics:

| Metric | What it measures |
|--------|-----------------|
| `WordCount` | Total words |
| `CodeBlockCount` / `CodeBlockRatio` | How much content is code examples |
| `CodeLanguages` | Languages used in fenced code blocks |
| `SentenceCount` / `ImperativeCount` / `ImperativeRatio` | How directive the language is |
| `InformationDensity` | Combined code density + imperative ratio |
| `StrongMarkers` / `WeakMarkers` / `InstructionSpecificity` | "must/always/never" vs "may/consider/could" |
| `SectionCount` / `ListItemCount` | Structural organization |

The information density formula adapts based on content:
- **With code blocks**: `(codeBlockRatio × 0.5) + (imperativeRatio × 0.5)`
- **Without code blocks**: Just `imperativeRatio` — prose-only skills aren't penalized

---

## 8. Cross-Language Contamination Detection

The `contamination` package detects when a skill mixes multiple programming ecosystems.

```bash
sed -n "1,80p" contamination/contamination.go
```

```output
// Package contamination detects cross-language and cross-technology
// contamination in skill content. It analyzes code block languages,
// technology references, and multi-interface tool usage to compute a
// contamination score indicating how likely a skill is to confuse an
// agent by mixing unrelated languages or domains.
package contamination

import (
	"math"
	"sort"
	"strings"

	"github.com/agent-ecosystem/skill-validator/types"
	"github.com/agent-ecosystem/skill-validator/util"
)

// multiInterfaceTools maps tool/platform names to the language identifiers
// commonly used with them. When a skill mentions one of these tools, multiple
// code languages are expected and should not be penalized as contamination.
var multiInterfaceTools = map[string][]string{
	"mongodb":       {"javascript", "python", "java", "csharp", "go", "ruby", "rust", "shell", "bash", "mongosh"},
	"aws":           {"python", "javascript", "typescript", "java", "go", "cli", "bash", "shell", "cloudformation", "terraform"},
	"docker":        {"yaml", "bash", "shell", "dockerfile", "python", "javascript"},
	"kubernetes":    {"yaml", "bash", "shell", "go", "python"},
	"redis":         {"python", "javascript", "java", "go", "ruby", "bash", "shell"},
	"postgresql":    {"sql", "python", "javascript", "java", "go", "ruby"},
	"mysql":         {"sql", "python", "javascript", "java", "go", "ruby"},
	"elasticsearch": {"json", "python", "javascript", "java", "curl", "bash"},
	"firebase":      {"javascript", "typescript", "python", "java", "swift", "kotlin", "dart"},
	"terraform":     {"hcl", "bash", "shell", "json", "yaml"},
	"graphql":       {"graphql", "javascript", "typescript", "python", "java", "go"},
	"grpc":          {"protobuf", "python", "go", "java", "javascript", "csharp"},
	"kafka":         {"java", "python", "go", "javascript", "scala"},
	"rabbitmq":      {"python", "java", "javascript", "go", "ruby"},
	"stripe":        {"python", "javascript", "ruby", "java", "go", "php", "curl"},
}

// languageCategories groups language identifiers into higher-level categories
// for detecting cross-contamination between unrelated language families.
var languageCategories = map[string]map[string]bool{
	"shell":      {"bash": true, "shell": true, "sh": true, "zsh": true, "fish": true, "powershell": true, "cmd": true, "bat": true},
	"javascript": {"javascript": true, "js": true, "typescript": true, "ts": true, "jsx": true, "tsx": true, "node": true},
	"python":     {"python": true, "py": true, "python3": true},
	"java":       {"java": true, "kotlin": true, "scala": true, "groovy": true},
	"systems":    {"c": true, "cpp": true, "c++": true, "rust": true, "go": true, "zig": true},
	"ruby":       {"ruby": true, "rb": true},
	"dotnet":     {"csharp": true, "cs": true, "fsharp": true, "vb": true},
	"config":     {"yaml": true, "yml": true, "json": true, "toml": true, "ini": true, "xml": true, "hcl": true},
	"query":      {"sql": true, "graphql": true, "cypher": true, "sparql": true},
	"markup":     {"html": true, "css": true, "scss": true, "sass": true, "less": true, "markdown": true, "md": true},
	"mobile":     {"swift": true, "kotlin": true, "dart": true, "objective-c": true, "objc": true},
}

// techPatterns maps framework and runtime names to their language category,
// used to detect technology references in prose that broaden scope.
var techPatterns = map[string]string{
	"node.js": "javascript",
	"react":   "javascript",
	"express": "javascript",
	"django":  "python",
	"flask":   "python",
	"fastapi": "python",
	"spring":  "java",
	"rails":   "ruby",
	"asp.net": "dotnet",
	".net":    "dotnet",
	"swift":   "mobile",
	"flutter": "mobile",
}

// applicationCategories lists language categories classified as application languages.
// These have high syntactic confusion risk with each other (per PLC research).
var applicationCategories = map[string]bool{
	"javascript": true,
	"python":     true,
	"java":       true,
	"systems":    true,
	"ruby":       true,
	"dotnet":     true,
	"mobile":     true,
```

```bash
grep -n "func Analyze" contamination/contamination.go
```

```output
117:func Analyze(name, content string, codeLanguages []string) *types.ContaminationReport {
```

```bash
sed -n "117,230p" contamination/contamination.go
```

```output
func Analyze(name, content string, codeLanguages []string) *types.ContaminationReport {
	if codeLanguages == nil {
		codeLanguages = []string{}
	}

	// Detect multi-interface tools
	multiTools := detectMultiInterfaceTools(name, content)

	// Analyze code block language diversity
	langCategories := getLanguageCategories(codeLanguages)

	// Detect additional technology references
	techRefs := detectTechnologyReferences(content)

	// Combine all scope indicators
	allScopes := make(map[string]bool)
	for cat := range langCategories {
		allScopes[cat] = true
	}
	for cat := range techRefs {
		allScopes[cat] = true
	}
	scopeBreadth := len(allScopes)

	// Detect language mismatch
	primaryCategory := findPrimaryCategory(codeLanguages)
	mismatchedCategories := make(map[string]bool)
	if primaryCategory != "" {
		for cat := range langCategories {
			if cat != primaryCategory {
				mismatchedCategories[cat] = true
			}
		}
	}
	languageMismatch := len(mismatchedCategories) > 0

	// Calculate contamination score
	factors := 0.0

	// Factor 1: Multi-interface tool (0.0 or 0.3)
	if len(multiTools) > 0 {
		factors += 0.3
	}

	// Factor 2: Language mismatch in code blocks (0.0-0.4)
	// Weight mismatches by syntactic similarity: application↔application mismatches
	// score higher than application↔auxiliary (per PLC research on language confusion).
	mismatchWeights := make(map[string]float64)
	if languageMismatch {
		weightedMismatch := 0.0
		for cat := range mismatchedCategories {
			w := mismatchWeight(primaryCategory, cat)
			mismatchWeights[cat] = w
			weightedMismatch += w
		}
		mismatchSeverity := math.Min(weightedMismatch/3.0, 1.0)
		factors += 0.4 * mismatchSeverity
	}

	// Factor 3: Scope breadth (0.0-0.3)
	if scopeBreadth > 2 {
		breadthScore := math.Min(float64(scopeBreadth-2)/4.0, 1.0)
		factors += 0.3 * breadthScore
	}

	score := util.RoundTo(math.Min(factors, 1.0), 4)

	// Contamination level
	level := "low"
	if score >= 0.5 {
		level = "high"
	} else if score >= 0.2 {
		level = "medium"
	}

	return &types.ContaminationReport{
		MultiInterfaceTools:  multiTools,
		CodeLanguages:        codeLanguages,
		LanguageCategories:   util.SortedKeys(langCategories),
		PrimaryCategory:      primaryCategory,
		MismatchedCategories: util.SortedKeys(mismatchedCategories),
		MismatchWeights:      mismatchWeights,
		LanguageMismatch:     languageMismatch,
		TechReferences:       util.SortedKeys(techRefs),
		ScopeBreadth:         scopeBreadth,
		ContaminationScore:   score,
		ContaminationLevel:   level,
	}
}

func detectMultiInterfaceTools(name, content string) []string {
	matches := make([]string, 0)
	nameLower := strings.ToLower(name)
	contentLower := strings.ToLower(content)

	for tool := range multiInterfaceTools {
		if strings.Contains(nameLower, tool) || strings.Contains(contentLower, tool) {
			matches = append(matches, tool)
		}
	}
	sort.Strings(matches)
	return matches
}

func getLanguageCategories(languages []string) map[string]bool {
	categories := make(map[string]bool)
	for _, lang := range languages {
		langLower := strings.ToLower(lang)
		for category, members := range languageCategories {
			if members[langLower] {
				categories[category] = true
				break
			}
		}
```

Contamination scoring uses three weighted factors:

```
score = 0.3 × multi_interface + 0.4 × language_mismatch + 0.3 × scope_breadth
```

- **Multi-interface tools** (0.3): If the skill mentions MongoDB, Docker, AWS, etc., multiple languages are expected — this _increases_ the score but is context-aware via the `multiInterfaceTools` allowlist
- **Language mismatch** (0.4): When code blocks use languages from different categories (e.g., Python + Java), weighted by syntactic similarity — application↔application mismatches score higher than application↔config
- **Scope breadth** (0.3): How many distinct technology categories are referenced (code + prose combined)

Levels: low (<0.2), medium (0.2-0.5), high (≥0.5)

This is thoughtful — a skill about Docker that uses bash + yaml + python isn't "contaminated," but a Python skill with random Java examples probably is.

---

## 9. LLM Scoring (Experimental)

The `judge` and `evaluate` packages handle LLM-based quality scoring.

### The Judge: LLM API Clients

```bash
cat -n judge/judge.go
```

```output
     1	// Package judge provides LLM-based quality scoring for skill files. It sends
     2	// SKILL.md and reference file content to an LLM judge that rates them on
     3	// dimensions like clarity, actionability, token efficiency, and novelty.
     4	// Results are cached per provider/model/file to avoid redundant API calls.
     5	//
     6	// # Stability
     7	//
     8	// This package is EXPERIMENTAL. Its API may change in minor releases without
     9	// a major version bump. See the project README for the full stability policy.
    10	package judge
    11	
    12	import (
    13		"context"
    14		"encoding/json"
    15		"fmt"
    16		"math"
    17		"regexp"
    18		"strings"
    19	
    20		"github.com/agent-ecosystem/skill-validator/types"
    21	)
    22	
    23	// SkillScores holds the LLM judge scores for a SKILL.md file.
    24	type SkillScores struct {
    25		Clarity            int     `json:"clarity"`
    26		Actionability      int     `json:"actionability"`
    27		TokenEfficiency    int     `json:"token_efficiency"`
    28		ScopeDiscipline    int     `json:"scope_discipline"`
    29		DirectivePrecision int     `json:"directive_precision"`
    30		Novelty            int     `json:"novelty"`
    31		Overall            float64 `json:"overall"`
    32		BriefAssessment    string  `json:"brief_assessment"`
    33		NovelInfo          string  `json:"novel_info,omitempty"`
    34	}
    35	
    36	// RefScores holds the LLM judge scores for a reference file.
    37	type RefScores struct {
    38		Clarity            int     `json:"clarity"`
    39		InstructionalValue int     `json:"instructional_value"`
    40		TokenEfficiency    int     `json:"token_efficiency"`
    41		Novelty            int     `json:"novelty"`
    42		SkillRelevance     int     `json:"skill_relevance"`
    43		Overall            float64 `json:"overall"`
    44		BriefAssessment    string  `json:"brief_assessment"`
    45		NovelInfo          string  `json:"novel_info,omitempty"`
    46	}
    47	
    48	// DimensionScores returns the ordered dimension scores for SKILL.md scoring.
    49	func (s *SkillScores) DimensionScores() []types.DimensionScore {
    50		return []types.DimensionScore{
    51			{Label: "Clarity", Value: s.Clarity},
    52			{Label: "Actionability", Value: s.Actionability},
    53			{Label: "Token Efficiency", Value: s.TokenEfficiency},
    54			{Label: "Scope Discipline", Value: s.ScopeDiscipline},
    55			{Label: "Directive Precision", Value: s.DirectivePrecision},
    56			{Label: "Novelty", Value: s.Novelty},
    57		}
    58	}
    59	
    60	// OverallScore returns the computed overall score.
    61	func (s *SkillScores) OverallScore() float64 { return s.Overall }
    62	
    63	// Assessment returns the brief assessment text.
    64	func (s *SkillScores) Assessment() string { return s.BriefAssessment }
    65	
    66	// NovelDetails returns novel information details, if any.
    67	func (s *SkillScores) NovelDetails() string { return s.NovelInfo }
    68	
    69	// DimensionScores returns the ordered dimension scores for reference file scoring.
    70	func (s *RefScores) DimensionScores() []types.DimensionScore {
    71		return []types.DimensionScore{
    72			{Label: "Clarity", Value: s.Clarity},
    73			{Label: "Instructional Value", Value: s.InstructionalValue},
    74			{Label: "Token Efficiency", Value: s.TokenEfficiency},
    75			{Label: "Novelty", Value: s.Novelty},
    76			{Label: "Skill Relevance", Value: s.SkillRelevance},
    77		}
    78	}
    79	
    80	// OverallScore returns the computed overall score.
    81	func (s *RefScores) OverallScore() float64 { return s.Overall }
    82	
    83	// Assessment returns the brief assessment text.
    84	func (s *RefScores) Assessment() string { return s.BriefAssessment }
    85	
    86	// NovelDetails returns novel information details, if any.
    87	func (s *RefScores) NovelDetails() string { return s.NovelInfo }
    88	
    89	var (
    90		skillDims = []string{"clarity", "actionability", "token_efficiency", "scope_discipline", "directive_precision", "novelty"}
    91		refDims   = []string{"clarity", "instructional_value", "token_efficiency", "novelty", "skill_relevance"}
    92	)
    93	
    94	// SkillDimensions returns the dimension names for SKILL.md scoring.
    95	func SkillDimensions() []string { return append([]string{}, skillDims...) }
    96	
    97	// RefDimensions returns the dimension names for reference file scoring.
    98	func RefDimensions() []string { return append([]string{}, refDims...) }
    99	
   100	// ---------------------------------------------------------------------------
   101	// Judge prompts
   102	// ---------------------------------------------------------------------------
   103	
   104	const skillJudgePrompt = `You are evaluating the quality of an "Agent Skill" — a markdown document that instructs an AI coding agent how to perform a specific task.  Score this skill on 6 dimensions, each from 1 (worst) to 5 (best). Use the full range — reserve 5 for genuinely excellent output and do not round up:
   105	
   106	**Scoring dimensions:**
   107	
   108	1. **Clarity** (1-5): How clear and unambiguous are the instructions? Are there vague or confusing passages? If the skill depends on specific tools, runtimes, or prerequisites, are they explicitly declared so an agent knows what must be available before proceeding?
   109	   - 1: Mostly vague, unclear instructions; an agent would frequently misinterpret intent
   110	   - 2: Several unclear passages that would cause an agent to guess or ask for clarification
   111	   - 3: Generally clear with some ambiguities; an agent could follow most instructions but would stumble on a few
   112	   - 4: Clear throughout with only minor phrasing that could be tightened; an agent would rarely misinterpret
   113	   - 5: Crystal clear, no room for misinterpretation; every instruction has exactly one reading; any dependencies are explicitly stated
   114	
   115	2. **Actionability** (1-5): How actionable are the instructions for an AI agent? Can an agent follow them step-by-step?
   116	   - 1: Abstract advice, no concrete steps; an agent could not act on these instructions
   117	   - 2: Mostly abstract with a few concrete steps scattered throughout
   118	   - 3: Mix of concrete and abstract guidance; an agent could act on roughly half the content directly
   119	   - 4: Mostly concrete and actionable with occasional abstract guidance that lacks specific steps
   120	   - 5: Highly specific, step-by-step instructions an agent can execute without interpretation
   121	
   122	3. **Token Efficiency** (1-5): How concise is the skill? Does every token earn its place in the context window, or is there redundant prose, boilerplate, or filler that could be trimmed without losing instructional value?
   123	   - 1: Extremely verbose, heavy boilerplate; could cut 50%+ without losing instructional value
   124	   - 2: Notably verbose; significant sections of redundant explanation, filler, or repeated content that could be cut
   125	   - 3: Reasonably concise with some unnecessary verbosity; ~20-30% could be trimmed
   126	   - 4: Concise with only minor redundancies; nearly every paragraph earns its place
   127	   - 5: Maximally concise — every sentence carries essential information; nothing to cut
   128	
   129	4. **Scope Discipline** (1-5): Does the skill stay tightly focused on its stated purpose and primary language/technology, or does it sprawl into adjacent domains, languages, or concerns that risk confusing the agent?
   130	   - 1: Sprawling scope, mixes many unrelated languages or domains; unclear what the skill is actually for
   131	   - 2: Covers its primary purpose but includes substantial tangential content in other languages or domains
   132	   - 3: Mostly focused with some tangential content that an agent might incorrectly apply to the wrong context
   133	   - 4: Well-focused on its purpose with only brief mentions of adjacent concerns that are clearly delineated
   134	   - 5: Tightly scoped to a single purpose and technology; no content an agent could misapply
   135	
   136	5. **Directive Precision** (1-5): Does the skill use precise, unambiguous directives (must, always, never, ensure) or does it hedge with vague suggestions (consider, may, could, possibly)? Are conditional sections clearly gated with explicit criteria for when to continue, skip, or abort?
   137	   - 1: Mostly vague suggestions and hedged language; an agent would not know what is required vs. optional
   138	   - 2: More hedging than precision; important instructions are often phrased as suggestions
   139	   - 3: Mix of precise directives and vague guidance; critical steps are usually precise but supporting guidance hedges
   140	   - 4: Mostly precise directives with occasional hedging on less critical points; conditional sections have reasonably clear gates
   141	   - 5: Consistently precise, imperative directives throughout; every instruction is unambiguous about whether it is required; conditional paths have explicit continue/abort criteria
   142	
   143	6. **Novelty** (1-5): How much of this skill's content provides information beyond what you would already know from training data? Does it convey project-specific conventions, proprietary APIs, internal workflows, or non-obvious domain knowledge — or does it mostly restate common programming knowledge you already have?
   144	   - 1: Almost entirely common knowledge any LLM would already know; standard library docs, basic patterns, introductory tutorials
   145	   - 2: Mostly common knowledge with a few pieces of genuinely new information (e.g., a specific version pin, one non-obvious convention) embedded in otherwise familiar content
   146	   - 3: Roughly equal mix of common knowledge and genuinely new information; the novel parts are useful but interspersed with content you already know well
   147	   - 4: Majority novel information — proprietary APIs, internal conventions, non-obvious gotchas — with some standard knowledge included for context or completeness
   148	   - 5: Predominantly novel; nearly every section provides information not available in training data (proprietary systems, unpublished APIs, organization-specific workflows)
   149	
   150	Respond with ONLY a JSON object in this exact format:
   151	{
   152	  "clarity": <1-5>,
   153	  "actionability": <1-5>,
   154	  "token_efficiency": <1-5>,
   155	  "scope_discipline": <1-5>,
   156	  "directive_precision": <1-5>,
   157	  "novelty": <1-5>,
   158	  "brief_assessment": "<1-2 sentence summary>"
   159	}`
   160	
   161	const refJudgePromptTemplate = `You are evaluating the quality of a **reference file** that accompanies an Agent Skill. Reference files are supplementary documents (examples, API docs, patterns, etc.) loaded alongside the main SKILL.md into an AI coding agent's context window.
   162	
   163	The parent skill's purpose is provided below so you can judge whether this reference supports it.
   164	
   165	**Parent skill:** %s
   166	**Parent description:** %s
   167	
   168	Score this reference file on 5 dimensions, each from 1 (worst) to 5 (best). Use the full range — reserve 5 for genuinely excellent output and do not round up:
   169	
   170	**Scoring dimensions:**
   171	
   172	1. **Clarity** (1-5): How clear and well-written is this reference? Can an AI agent easily parse and apply the information?
   173	   - 1: Confusing, poorly formatted, hard to extract useful information
   174	   - 2: Partially readable but disorganized; an agent would need to work to extract key information
   175	   - 3: Generally clear with some ambiguities or formatting issues; usable but not optimized for agent consumption
   176	   - 4: Well-structured and clear with only minor formatting or organizational issues
   177	   - 5: Crystal clear, well-structured, easy for an agent to consume; information hierarchy is immediately apparent
   178	
   179	2. **Instructional Value** (1-5): Does this reference provide concrete, directly-applicable examples, patterns, or API signatures that an agent can use — or is it abstract and theoretical?
   180	   - 1: Abstract descriptions with no concrete examples or patterns; an agent cannot act on this content
   181	   - 2: Mostly abstract with a few concrete examples that are insufficient for practical use
   182	   - 3: Mix of concrete examples and abstract explanations; an agent could use some content directly but would need to fill gaps
   183	   - 4: Mostly concrete and directly applicable with occasional abstract sections that lack working examples
   184	   - 5: Rich with directly-applicable code examples, patterns, and signatures; an agent could use the content as-is
   185	
   186	3. **Token Efficiency** (1-5): Does every token in this reference earn its place in the context window? Is the content concise, or bloated with redundant explanations, excessive boilerplate, or content that could be significantly compressed?
   187	   - 1: Extremely verbose; could cut 50%%+ without losing useful information
   188	   - 2: Notably verbose; significant redundancy, repeated explanations, or boilerplate that inflates token count
   189	   - 3: Reasonably concise with some unnecessary verbosity; ~20-30%% could be trimmed
   190	   - 4: Concise with only minor redundancies; nearly every section earns its token budget
   191	   - 5: Maximally concise — every section carries essential information; nothing to cut
   192	
   193	4. **Novelty** (1-5): How much of this reference provides information beyond what you would already know from training data? Does it document proprietary APIs, internal conventions, non-obvious gotchas, or uncommon patterns — or does it mostly restate standard documentation you already have access to?
   194	   - 1: Almost entirely common knowledge (standard library docs, well-known patterns, basic tutorials)
   195	   - 2: Mostly common knowledge with a few novel details (e.g., specific version requirements, one unusual configuration) embedded in otherwise familiar content
   196	   - 3: Roughly equal mix of common knowledge and genuinely new information; the novel parts are useful but interspersed with familiar documentation
   197	   - 4: Majority novel information — proprietary API details, internal conventions, non-obvious gotchas — with some standard content included for context
   198	   - 5: Predominantly novel; nearly every section documents proprietary APIs, unpublished interfaces, or organization-specific patterns not in training data
   199	
   200	5. **Skill Relevance** (1-5): How directly does this reference file support the parent skill's stated purpose? Does every section contribute to what the skill is trying to teach the agent, or does it include tangential content?
   201	   - 1: Mostly unrelated to the parent skill's purpose; appears to be a generic reference bundled without curation
   202	   - 2: Partially relevant but includes substantial tangential content unrelated to the skill's stated purpose
   203	   - 3: Generally relevant with some tangential sections that an agent would need to filter out
   204	   - 4: Clearly relevant to the skill's purpose with only minor tangential content
   205	   - 5: Every section directly supports the parent skill's purpose; tightly curated for the skill's specific use case
   206	
   207	Respond with ONLY a JSON object in this exact format:
   208	{
   209	  "clarity": <1-5>,
   210	  "instructional_value": <1-5>,
   211	  "token_efficiency": <1-5>,
   212	  "novelty": <1-5>,
   213	  "skill_relevance": <1-5>,
   214	  "brief_assessment": "<1-2 sentence summary>"
   215	}`
   216	
   217	const novelInfoPrompt = `You just scored a document on novelty. It scored high (3+/5), meaning it likely contains project-specific or proprietary information not available in public training data.
   218	
   219	In 1-2 sentences, identify which specific details are novel — for example, proprietary API names or signatures, internal conventions, unpublished workflows, organization-specific patterns, or non-standard configuration details. Focus on what a human reviewer should fact-check. Respond with plain text only, no JSON.`
   220	
   221	// DefaultMaxContentLen is the default maximum content length sent to the judge (characters).
   222	// Use 0 to disable truncation.
   223	const DefaultMaxContentLen = 8000
   224	
   225	// ScoreSkill sends a SKILL.md's content to the LLM judge and returns parsed scores.
   226	// maxLen controls content truncation (0 = no truncation).
   227	func ScoreSkill(ctx context.Context, content string, client LLMClient, maxLen int) (*SkillScores, error) {
   228		userContent := formatUserContent(content, maxLen)
   229		text, err := client.Complete(ctx, skillJudgePrompt, userContent)
   230		if err != nil {
   231			return nil, fmt.Errorf("scoring SKILL.md: %w", err)
   232		}
   233	
   234		scores, err := parseSkillScores(text)
   235		if err != nil {
   236			return nil, err
   237		}
   238	
   239		// Retry if dimensions are missing
   240		missing := missingSkillDims(scores)
   241		if len(missing) > 0 {
   242			retryPrompt := skillJudgePrompt + "\n\nIMPORTANT: Your response MUST include ALL dimensions. You MUST include these keys in your JSON: " + strings.Join(missing, ", ")
   243			text, err = client.Complete(ctx, retryPrompt, userContent)
   244			if err != nil {
   245				// Return partial scores rather than failing entirely
   246				scores.Overall = computeSkillOverall(scores)
   247				return scores, nil
   248			}
   249			retry, err := parseSkillScores(text)
   250			if err == nil {
   251				scores = mergeSkillScores(scores, retry)
   252			}
   253		}
   254	
   255		scores.Overall = computeSkillOverall(scores)
   256	
   257		// Follow-up call for high-novelty skills
   258		if scores.Novelty >= 3 {
   259			novelText, err := client.Complete(ctx, novelInfoPrompt, userContent)
   260			if err == nil {
   261				scores.NovelInfo = strings.TrimSpace(novelText)
   262			}
   263		}
   264	
   265		return scores, nil
   266	}
   267	
   268	// ScoreReference sends a reference file's content to the LLM judge and returns parsed scores.
   269	// maxLen controls content truncation (0 = no truncation).
   270	func ScoreReference(ctx context.Context, content, skillName, skillDesc string, client LLMClient, maxLen int) (*RefScores, error) {
   271		if skillName == "" {
   272			skillName = "(unnamed skill)"
   273		}
   274		if skillDesc == "" {
   275			skillDesc = "(no description provided)"
   276		}
   277	
   278		systemPrompt := fmt.Sprintf(refJudgePromptTemplate, skillName, skillDesc)
   279		userContent := formatUserContent(content, maxLen)
   280	
   281		text, err := client.Complete(ctx, systemPrompt, userContent)
   282		if err != nil {
   283			return nil, fmt.Errorf("scoring reference file: %w", err)
   284		}
   285	
   286		scores, err := parseRefScores(text)
   287		if err != nil {
   288			return nil, err
   289		}
   290	
   291		// Retry if dimensions are missing
   292		missing := missingRefDims(scores)
   293		if len(missing) > 0 {
   294			retryPrompt := systemPrompt + "\n\nIMPORTANT: Your response MUST include ALL dimensions. You MUST include these keys in your JSON: " + strings.Join(missing, ", ")
   295			text, err = client.Complete(ctx, retryPrompt, userContent)
   296			if err != nil {
   297				scores.Overall = computeRefOverall(scores)
   298				return scores, nil
   299			}
   300			retry, err := parseRefScores(text)
   301			if err == nil {
   302				scores = mergeRefScores(scores, retry)
   303			}
   304		}
   305	
   306		scores.Overall = computeRefOverall(scores)
   307	
   308		// Follow-up call for high-novelty references
   309		if scores.Novelty >= 3 {
   310			novelText, err := client.Complete(ctx, novelInfoPrompt, userContent)
   311			if err == nil {
   312				scores.NovelInfo = strings.TrimSpace(novelText)
   313			}
   314		}
   315	
   316		return scores, nil
   317	}
   318	
   319	// AggregateRefScores computes mean scores across multiple reference file results.
   320	func AggregateRefScores(results []*RefScores) *RefScores {
   321		if len(results) == 0 {
   322			return nil
   323		}
   324	
   325		agg := &RefScores{}
   326		for _, r := range results {
   327			agg.Clarity += r.Clarity
   328			agg.InstructionalValue += r.InstructionalValue
   329			agg.TokenEfficiency += r.TokenEfficiency
   330			agg.Novelty += r.Novelty
   331			agg.SkillRelevance += r.SkillRelevance
   332		}
   333	
   334		n := len(results)
   335		agg.Clarity = (agg.Clarity + n/2) / n
   336		agg.InstructionalValue = (agg.InstructionalValue + n/2) / n
   337		agg.TokenEfficiency = (agg.TokenEfficiency + n/2) / n
   338		agg.Novelty = (agg.Novelty + n/2) / n
   339		agg.SkillRelevance = (agg.SkillRelevance + n/2) / n
   340		agg.Overall = computeRefOverall(agg)
   341		return agg
   342	}
   343	
   344	// --- Internal helpers ---
   345	
   346	func formatUserContent(content string, maxLen int) string {
   347		if maxLen > 0 && len(content) > maxLen {
   348			content = content[:maxLen]
   349		}
   350		return "CONTENT TO EVALUATE:\n\n" + content
   351	}
   352	
   353	var jsonObjectRe = regexp.MustCompile(`\{[^{}]+\}`)
   354	
   355	// extractJSON finds the first JSON object in the response text.
   356	func extractJSON(text string) (string, error) {
   357		text = strings.TrimSpace(text)
   358		if strings.HasPrefix(text, "{") {
   359			// Try to parse the whole thing first
   360			end := strings.LastIndex(text, "}")
   361			if end >= 0 {
   362				candidate := text[:end+1]
   363				if json.Valid([]byte(candidate)) {
   364					return candidate, nil
   365				}
   366			}
   367		}
   368	
   369		// Search for embedded JSON object
   370		match := jsonObjectRe.FindString(text)
   371		if match != "" && json.Valid([]byte(match)) {
   372			return match, nil
   373		}
   374	
   375		return "", fmt.Errorf("no valid JSON object found in response: %.100s", text)
   376	}
   377	
   378	func parseSkillScores(text string) (*SkillScores, error) {
   379		jsonStr, err := extractJSON(text)
   380		if err != nil {
   381			return nil, err
   382		}
   383	
   384		var scores SkillScores
   385		if err := json.Unmarshal([]byte(jsonStr), &scores); err != nil {
   386			return nil, fmt.Errorf("parsing skill scores: %w", err)
   387		}
   388	
   389		return &scores, nil
   390	}
   391	
   392	func parseRefScores(text string) (*RefScores, error) {
   393		jsonStr, err := extractJSON(text)
   394		if err != nil {
   395			return nil, err
   396		}
   397	
   398		var scores RefScores
   399		if err := json.Unmarshal([]byte(jsonStr), &scores); err != nil {
   400			return nil, fmt.Errorf("parsing reference scores: %w", err)
   401		}
   402	
   403		return &scores, nil
   404	}
   405	
   406	func missingSkillDims(s *SkillScores) []string {
   407		var missing []string
   408		if s.Clarity == 0 {
   409			missing = append(missing, "clarity")
   410		}
   411		if s.Actionability == 0 {
   412			missing = append(missing, "actionability")
   413		}
   414		if s.TokenEfficiency == 0 {
   415			missing = append(missing, "token_efficiency")
   416		}
   417		if s.ScopeDiscipline == 0 {
   418			missing = append(missing, "scope_discipline")
   419		}
   420		if s.DirectivePrecision == 0 {
   421			missing = append(missing, "directive_precision")
   422		}
   423		if s.Novelty == 0 {
   424			missing = append(missing, "novelty")
   425		}
   426		return missing
   427	}
   428	
   429	func missingRefDims(s *RefScores) []string {
   430		var missing []string
   431		if s.Clarity == 0 {
   432			missing = append(missing, "clarity")
   433		}
   434		if s.InstructionalValue == 0 {
   435			missing = append(missing, "instructional_value")
   436		}
   437		if s.TokenEfficiency == 0 {
   438			missing = append(missing, "token_efficiency")
   439		}
   440		if s.Novelty == 0 {
   441			missing = append(missing, "novelty")
   442		}
   443		if s.SkillRelevance == 0 {
   444			missing = append(missing, "skill_relevance")
   445		}
   446		return missing
   447	}
   448	
   449	func computeSkillOverall(s *SkillScores) float64 {
   450		vals := []int{s.Clarity, s.Actionability, s.TokenEfficiency, s.ScopeDiscipline, s.DirectivePrecision, s.Novelty}
   451		return computeMean(vals)
   452	}
   453	
   454	func computeRefOverall(s *RefScores) float64 {
   455		vals := []int{s.Clarity, s.InstructionalValue, s.TokenEfficiency, s.Novelty, s.SkillRelevance}
   456		return computeMean(vals)
   457	}
   458	
   459	func computeMean(vals []int) float64 {
   460		var sum int
   461		var count int
   462		for _, v := range vals {
   463			if v > 0 {
   464				sum += v
   465				count++
   466			}
   467		}
   468		if count == 0 {
   469			return 0
   470		}
   471		return math.Round(float64(sum)/float64(count)*100) / 100
   472	}
   473	
   474	func mergeSkillScores(base, retry *SkillScores) *SkillScores {
   475		// If retry is complete, prefer it
   476		if len(missingSkillDims(retry)) == 0 {
   477			retry.BriefAssessment = coalesce(retry.BriefAssessment, base.BriefAssessment)
   478			return retry
   479		}
   480		// Fill gaps from retry
   481		if base.Clarity == 0 && retry.Clarity != 0 {
   482			base.Clarity = retry.Clarity
   483		}
   484		if base.Actionability == 0 && retry.Actionability != 0 {
   485			base.Actionability = retry.Actionability
   486		}
   487		if base.TokenEfficiency == 0 && retry.TokenEfficiency != 0 {
   488			base.TokenEfficiency = retry.TokenEfficiency
   489		}
   490		if base.ScopeDiscipline == 0 && retry.ScopeDiscipline != 0 {
   491			base.ScopeDiscipline = retry.ScopeDiscipline
   492		}
   493		if base.DirectivePrecision == 0 && retry.DirectivePrecision != 0 {
   494			base.DirectivePrecision = retry.DirectivePrecision
   495		}
   496		if base.Novelty == 0 && retry.Novelty != 0 {
   497			base.Novelty = retry.Novelty
   498		}
   499		if base.BriefAssessment == "" {
   500			base.BriefAssessment = retry.BriefAssessment
   501		}
   502		return base
   503	}
   504	
   505	func mergeRefScores(base, retry *RefScores) *RefScores {
   506		if len(missingRefDims(retry)) == 0 {
   507			retry.BriefAssessment = coalesce(retry.BriefAssessment, base.BriefAssessment)
   508			return retry
   509		}
   510		if base.Clarity == 0 && retry.Clarity != 0 {
   511			base.Clarity = retry.Clarity
   512		}
   513		if base.InstructionalValue == 0 && retry.InstructionalValue != 0 {
   514			base.InstructionalValue = retry.InstructionalValue
   515		}
   516		if base.TokenEfficiency == 0 && retry.TokenEfficiency != 0 {
   517			base.TokenEfficiency = retry.TokenEfficiency
   518		}
   519		if base.Novelty == 0 && retry.Novelty != 0 {
   520			base.Novelty = retry.Novelty
   521		}
   522		if base.SkillRelevance == 0 && retry.SkillRelevance != 0 {
   523			base.SkillRelevance = retry.SkillRelevance
   524		}
   525		if base.BriefAssessment == "" {
   526			base.BriefAssessment = retry.BriefAssessment
   527		}
   528		return base
   529	}
   530	
   531	func coalesce(a, b string) string {
   532		if a != "" {
   533			return a
   534		}
   535		return b
   536	}
```

The judge package is the most complex. Key design:

**Two scoring rubrics:**

| SKILL.md (6 dimensions) | Reference files (5 dimensions) |
|--------------------------|-------------------------------|
| Clarity | Clarity |
| Actionability | Instructional Value |
| Token Efficiency | Token Efficiency |
| Scope Discipline | Novelty |
| Directive Precision | Skill Relevance |
| Novelty | |

**Reliability mechanisms:**
- Content truncated to 8,000 chars by default (configurable)
- JSON extraction with fallback regex (`\{[^{}]+\}`)
- **Retry on missing dimensions**: If the LLM omits a dimension, it re-prompts with explicit instructions
- **Merge logic**: Fills gaps from retry while preserving original scores
- **Novel info follow-up**: Skills scoring ≥3 on Novelty get a second LLM call asking "what specifically is novel?" for human fact-checking

**Overall score**: Simple mean of non-zero dimensions (rounded to 2 decimal places).

---

## 10. Orchestration

The `orchestrate` package ties all checks together into a single `RunAllChecks` call.

We already saw `orchestrate.go` in full. Its key role:

1. Runs structure validation (if enabled)
2. Loads the skill (with fallback to raw content on parse failure)
3. Runs link, content, and contamination checks (each independently gated)
4. Runs reference file analysis when content or contamination are enabled
5. Tallies results

The orchestrator is designed for both CLI and library use — the `Options` struct makes it configurable without flag parsing.

---

## 11. Report Output

The `report` package handles formatting results for different consumers.

```bash
sed -n "1,50p" report/report.go
```

```output
// Package report formats and prints validation and scoring results. It
// supports colored terminal output, GitHub Actions annotations, JSON, and
// Markdown output formats.
package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/agent-ecosystem/skill-validator/types"
	"github.com/agent-ecosystem/skill-validator/util"
)

// Shorthand aliases for ANSI color constants to keep format strings compact.
const (
	colorReset  = util.ColorReset
	colorRed    = util.ColorRed
	colorGreen  = util.ColorGreen
	colorYellow = util.ColorYellow
	colorCyan   = util.ColorCyan
	colorBold   = util.ColorBold
)

// Print writes a human-readable validation report to w. When perFile is true,
// per-file content and contamination analysis sections are included.
func Print(w io.Writer, r *types.Report, perFile bool) {
	_, _ = fmt.Fprintf(w, "\n%sValidating skill: %s%s\n", colorBold, r.SkillDir, colorReset)

	categories, grouped := groupByCategory(r.Results)

	for _, cat := range categories {
		_, _ = fmt.Fprintf(w, "\n%s%s%s\n", colorBold, cat, colorReset)
		for _, res := range grouped[cat] {
			icon, color := formatLevel(res.Level)
			_, _ = fmt.Fprintf(w, "  %s%s %s%s\n", color, icon, res.Message, colorReset)
		}
	}

	// Token counts
	if len(r.TokenCounts) > 0 {
		_, _ = fmt.Fprintf(w, "\n%sTokens%s\n", colorBold, colorReset)

		maxFileLen := len("Total")
		for _, tc := range r.TokenCounts {
			if len(tc.File) > maxFileLen {
				maxFileLen = len(tc.File)
			}
		}

```

```bash
sed -n "1,30p" report/annotations.go
```

```output
package report

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/agent-ecosystem/skill-validator/types"
)

// PrintAnnotations writes GitHub Actions workflow command annotations for
// errors and warnings in the report. Pass and Info results are skipped.
// workDir is the working directory used to compute relative file paths;
// in CI this is typically the repository root.
func PrintAnnotations(w io.Writer, r *types.Report, workDir string) {
	for _, res := range r.Results {
		line := formatAnnotation(r.SkillDir, res, workDir)
		if line != "" {
			_, _ = fmt.Fprintln(w, line)
		}
	}
}

// PrintMultiAnnotations writes annotations for all skills in a multi-report.
func PrintMultiAnnotations(w io.Writer, mr *types.MultiReport, workDir string) {
	for _, r := range mr.Skills {
		PrintAnnotations(w, r, workDir)
	}
}

```

Four output formats:

| Format | File | Use case |
|--------|------|----------|
| **Text** | `report.go` | Terminal with ANSI colors |
| **JSON** | `json.go` | Machine-readable, piping to other tools |
| **Markdown** | `markdown.go` | Documentation, PR comments |
| **Annotations** | `annotations.go` | GitHub Actions `::error file=...::message` |

The annotations format is particularly clever for CI — results appear as inline annotations on the PR diff, pointing to specific files and lines.

---

## 12. Dependencies and Build

### go.mod (minimal dependency surface)

```bash
cat -n go.mod
```

```output
     1	module github.com/agent-ecosystem/skill-validator
     2	
     3	go 1.25.5
     4	
     5	require (
     6		github.com/spf13/cobra v1.10.2
     7		github.com/tiktoken-go/tokenizer v0.7.0
     8		gopkg.in/yaml.v3 v3.0.1
     9	)
    10	
    11	require (
    12		github.com/dlclark/regexp2 v1.11.5 // indirect
    13		github.com/inconshreveable/mousetrap v1.1.0 // indirect
    14		github.com/spf13/pflag v1.0.9 // indirect
    15	)
```

Only **3 direct dependencies** — an impressively minimal surface area:

- **cobra** — CLI framework (industry standard)
- **tiktoken-go** — Token counting (o200k_base encoding)
- **yaml.v3** — YAML frontmatter parsing

No CGO. Pure Go. Builds for darwin/linux × amd64/arm64.

---

## 13. Test Coverage

```bash
go test -cover ./... 2>&1 | grep -v "no test files"
```

```output
ok  	github.com/agent-ecosystem/skill-validator/cmd	1.688s	coverage: 27.7% of statements
	github.com/agent-ecosystem/skill-validator/cmd/skill-validator		coverage: 0.0% of statements
ok  	github.com/agent-ecosystem/skill-validator/contamination	(cached)	coverage: 97.9% of statements
ok  	github.com/agent-ecosystem/skill-validator/content	(cached)	coverage: 98.4% of statements
ok  	github.com/agent-ecosystem/skill-validator/evaluate	(cached)	coverage: 97.1% of statements
ok  	github.com/agent-ecosystem/skill-validator/judge	(cached)	coverage: 90.5% of statements
ok  	github.com/agent-ecosystem/skill-validator/links	(cached)	coverage: 95.1% of statements
ok  	github.com/agent-ecosystem/skill-validator/orchestrate	(cached)	coverage: 98.7% of statements
ok  	github.com/agent-ecosystem/skill-validator/report	(cached)	coverage: 94.6% of statements
ok  	github.com/agent-ecosystem/skill-validator/skill	(cached)	coverage: 94.8% of statements
ok  	github.com/agent-ecosystem/skill-validator/skillcheck	(cached)	coverage: 95.0% of statements
ok  	github.com/agent-ecosystem/skill-validator/structure	(cached)	coverage: 94.3% of statements
ok  	github.com/agent-ecosystem/skill-validator/types	(cached)	coverage: 100.0% of statements
ok  	github.com/agent-ecosystem/skill-validator/util	(cached)	coverage: 96.0% of statements
```

```bash
ls testdata/
```

```output
broken-frontmatter
invalid-skill
multi-skill
rich-skill
valid-skill
warnings-only-skill
```

Coverage is excellent across core packages (94-100%), with the main gap being `cmd/` at 27.7%.

Test fixtures in `testdata/` cover multiple scenarios:
- `valid-skill` — happy path
- `invalid-skill` — missing SKILL.md, bad structure
- `broken-frontmatter` — malformed YAML
- `warnings-only-skill` — no errors, only warnings
- `multi-skill` — directory with multiple skill subdirectories
- `rich-skill` — full-featured skill with references, scripts, assets

Tests are well-organized:
- Shared helpers in `structure/helpers_test.go` (writeFile, writeSkill, requireResult, etc.)
- Example tests serve as documentation
- Integration tests for exit codes
- Race detection enabled in CI (`-race`)

---

## 14. Concerns and Standards Adherence

### What's done well

1. **Clean architecture**: Flat packages with clear boundaries. Every package has a single responsibility. The separation between validation, analysis, scoring, and reporting is excellent.

2. **Minimal dependencies**: Only 3 direct dependencies. No framework bloat.

3. **Error messages are actionable**: Structure warnings explain _why_ something is wrong and suggest _what to do_ (e.g., "move it into references/ or assets/"). This is unusually good for a validation tool.

4. **Test coverage**: 94%+ across all core packages with race detection.

5. **Exit code design**: Clean semantic exit codes (0/1/2/3) with `--strict` mode for CI.

6. **Library-first design**: The `orchestrate` package makes this usable as a Go library, not just a CLI.

7. **BFS orphan detection**: The reachability analysis with Python import resolution is sophisticated and practical.

### Concerns

#### Security (tracked issues)

| Issue | Severity | What |
|-------|----------|------|
| [#1](https://github.com/x3c3/skill-validator/issues/1) | HIGH | `io.ReadAll` on LLM responses without size limits |
| [#2](https://github.com/x3c3/skill-validator/issues/2) | HIGH | Missing context propagation in link checker |
| [#3](https://github.com/x3c3/skill-validator/issues/3) | HIGH | Symlink following in DetectSkills |
| [#4](https://github.com/x3c3/skill-validator/issues/4) | MEDIUM | Unbounded file reads |
| [#5](https://github.com/x3c3/skill-validator/issues/5) | MEDIUM | API error bodies not truncated |
| [#6](https://github.com/x3c3/skill-validator/issues/6) | MEDIUM | Cache dir/file permissions too open |
| [#7](https://github.com/x3c3/skill-validator/issues/7) | MEDIUM | SSRF risk in link validation |

#### Code quality

- **`cmd/` coverage at 27.7%** is the biggest gap — flag combinations and edge cases are largely untested ([#9](https://github.com/x3c3/skill-validator/issues/9))
- **No fuzz testing** — `splitFrontmatter` and `ExtractLinks` are parsing user input and would benefit from `testing.F` targets ([#8](https://github.com/x3c3/skill-validator/issues/8))
- **Unnecessary mutex** in link checker — indexed writes to a pre-allocated slice are goroutine-safe ([#10](https://github.com/x3c3/skill-validator/issues/10))
- **Fragile model detection** in `useMaxCompletionTokens` — prefix match on "o" catches too broadly ([#11](https://github.com/x3c3/skill-validator/issues/11))
- **`rootTextFiles()` naming** is misleading — it returns only extraneous files, not all root text files (noted in project memory)

#### Community standards

- **Go version 1.25.5**: Uses the latest Go features (`strings.SplitSeq`, `range` over integers). This is fine for a standalone tool but limits library adoption by projects on older Go versions.
- **Module path mismatch**: The module is `github.com/agent-ecosystem/skill-validator` but the repo is at `x3c3/skill-validator` (fork). This is expected for a fork but worth noting.
- **EXPERIMENTAL package**: The `judge` package is marked experimental in its doc comment but has no build tag or separate module to prevent accidental API reliance. A `// Deprecated:` annotation or separate module would be more conventional.
- **golangci-lint config** uses a solid set of linters (errcheck, govet, staticcheck, unused) with gofumpt formatting. The linter set could be expanded (e.g., `gosec` for security, `gocritic` for style) but the current set is reasonable.

### Overall verdict

This is a well-engineered, focused Go project with excellent test coverage and clean architecture. The security concerns are relevant for CI/untrusted-input usage but are not critical for the tool's primary use case of validating your own skills locally. The main improvement areas are defensive input validation and expanding the `cmd/` test coverage.

