package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Totals counts paths and findings.
type Totals struct {
	Paths     int `json:"paths"`
	Identical int `json:"identical"`
	Failing   int `json:"failingPaths"`
	Missing   int `json:"missing"`
	Changed   int `json:"changed"`
	Extra     int `json:"extra"`
	Expected  int `json:"expected"`
	// RefBroken counts cases that must succeed where the reference answered non-2xx.
	RefBroken int `json:"refBroken"`
	OkCases   int `json:"okCases"`
}

// Report is the machine-readable output.
type Report struct {
	Generated   string         `json:"generated"`
	DurationSec float64        `json:"durationSeconds"`
	Ref         string         `json:"ref"`
	New         string         `json:"new"`
	Mode        string         `json:"mode"`
	Totals      Totals         `json:"totals"`
	ExpectedBy  map[string]int `json:"expectedByRule"`
	Rules       []Rule         `json:"rules"`
	Results     []Result       `json:"results"`
	Load        []LoadResult   `json:"load,omitempty"`
}

var reRuleID = regexp.MustCompile(`^(E\d+):`)

func buildReport(cfg *config, results []Result, loads []LoadResult, started time.Time) *Report {
	rep := &Report{
		Generated: time.Now().UTC().Format(time.RFC3339), DurationSec: time.Since(started).Seconds(),
		Ref: cfg.Ref, New: cfg.New, Mode: cfg.Mode, Results: results, Load: loads, ExpectedBy: map[string]int{},
	}
	rep.Totals.Paths = len(results)
	for _, r := range results {
		if r.Spec.Expect == "ok" || r.Spec.Expect == "new" || r.Spec.Expect == "" {
			rep.Totals.OkCases++
			if r.RefStatus/100 != 2 && r.Spec.Expect != "new" && !cfg.refLacks(r.Spec.Path) {
				rep.Totals.RefBroken++
			}
		}
		fail, any := false, false
		for _, f := range r.Findings {
			any = true
			switch f.Kind {
			case KindMissing:
				rep.Totals.Missing++
				fail = true
			case KindChanged:
				rep.Totals.Changed++
				fail = true
			case KindExtra:
				rep.Totals.Extra++
			case KindExpected:
				rep.Totals.Expected++
				if m := reRuleID.FindStringSubmatch(f.Note); m != nil {
					rep.ExpectedBy[m[1]]++
				}
			}
		}
		if fail {
			rep.Totals.Failing++
		}
		if !any {
			rep.Totals.Identical++
		}
	}
	for _, r := range expectedRules {
		rep.Rules = append(rep.Rules, r)
	}
	sort.Slice(rep.Rules, func(i, j int) bool { return ruleNum(rep.Rules[i].ID) < ruleNum(rep.Rules[j].ID) })
	return rep
}

func ruleNum(id string) int {
	var n int
	fmt.Sscanf(id, "E%d", &n)
	return n
}

func writeReport(rep *Report, mdPath, jsonPath string) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(jsonPath, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(mdPath, []byte(rep.Summary()), 0o644)
}

const maxListed = 60

// Summary is the short human report.
func (rep *Report) Summary() string {
	var b strings.Builder
	t := rep.Totals
	fmt.Fprintf(&b, "# Insight comparison\n\nref `%s`\nnew `%s`\nmode %s, %s, %.0fs\n\n", rep.Ref, rep.New, rep.Mode, rep.Generated, rep.DurationSec)
	verdict := "PASS"
	if t.Failing > 0 {
		verdict = "FAIL"
	}
	if t.OkCases > 0 && t.RefBroken*2 > t.OkCases {
		fmt.Fprintf(&b, "**REFERENCE BROKEN**: the reference answered non-2xx on %d of %d cases that must succeed. Check -ref (host, port, prefix); this run proves nothing.\n\n", t.RefBroken, t.OkCases)
	}
	fmt.Fprintf(&b, "**%s**: %d paths, %d identical, %d with missing/changed (%d missing, %d changed), %d extra, %d expected\n\n",
		verdict, t.Paths, t.Identical, t.Failing, t.Missing, t.Changed, t.Extra, t.Expected)

	b.WriteString("Every finding, expected and extra ones included, is in the JSON report; this summary lists only failures.\n\n")
	if len(rep.ExpectedBy) > 0 {
		b.WriteString("## Expected differences\n\n")
		for _, r := range rep.Rules {
			if n := rep.ExpectedBy[r.ID]; n > 0 {
				fmt.Fprintf(&b, "- %s x%d (%s): %s\n", r.ID, n, r.Cite, r.Text)
			}
		}
		b.WriteString("\n")
	}

	listed := 0
	b.WriteString("## Failing differences\n\n")
	for _, r := range rep.Results {
		for _, f := range r.Findings {
			if !f.Failing() {
				continue
			}
			if listed == maxListed {
				fmt.Fprintf(&b, "- ... more in the JSON report\n")
			}
			listed++
			if listed > maxListed {
				continue
			}
			fmt.Fprintf(&b, "- [%s] `%s` %s: ref %s, new %s", f.Kind, f.Path, f.Pointer, f.Ref, f.Got)
			if f.Note != "" {
				fmt.Fprintf(&b, " (%s)", f.Note)
			}
			b.WriteString("\n")
		}
	}
	if listed == 0 {
		b.WriteString("none\n")
	}

	extra := map[string]int{}
	for _, r := range rep.Results {
		for _, f := range r.Findings {
			if f.Kind == KindExtra {
				extra[genericPointer(f.Pointer)]++
			}
		}
	}
	if len(extra) > 0 {
		b.WriteString("\n## Extra fields on the listener (not failures)\n\n")
		keys := make([]string, 0, len(extra))
		for k := range extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i == 25 {
				b.WriteString("- ... more in the JSON report\n")
				break
			}
			fmt.Fprintf(&b, "- `%s` x%d\n", k, extra[k])
		}
	}

	cors := 0
	for _, r := range rep.Results {
		if r.CORSRef != "" && r.CORSNew != "" {
			cors++
		}
	}
	fmt.Fprintf(&b, "\n## CORS\n\nAccess-Control-Allow-Origin present on both sides for %d of %d paths.", cors, t.Paths)
	for _, r := range rep.Results {
		if r.LinkNew != "" {
			fmt.Fprintf(&b, " Link header on new: `%s`.", r.LinkNew)
			break
		}
	}
	b.WriteString("\n")

	if len(rep.Load) > 0 {
		b.WriteString("\n## Latency under parallel load (ms)\n\n| path | ref p50 | ref p95 | ref fail | new p50 | new p95 | new fail |\n|---|---|---|---|---|---|---|\n")
		for _, l := range rep.Load {
			fmt.Fprintf(&b, "| `%s` | %.0f | %.0f | %d/%d | %.0f | %.0f | %d/%d |\n", truncate(l.Path, 60),
				l.Ref.P50ms, l.Ref.P95ms, l.Ref.Requests-l.Ref.OK, l.Ref.Requests,
				l.New.P50ms, l.New.P95ms, l.New.Requests-l.New.OK, l.New.Requests)
		}
	}
	return b.String()
}

var reIdx = regexp.MustCompile(`/(\d+|[0-9a-f]{64})(/|$)`)

// genericPointer folds array indexes and txids so one extra field counts once.
func genericPointer(p string) string {
	for i := 0; i < 3; i++ {
		p = reIdx.ReplaceAllString(p, "/*$2")
	}
	return p
}
