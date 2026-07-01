// AshMaize Eval grader.
//
// Runs each scenario's request through BOTH the candidate implementation (-agent-bin)
// and the reference oracle (-oracle-bin), then scores by exact output match on the
// fields the scenario names. Adversarial scenarios instead require the candidate to
// reject the input (non-zero exit, or an {"error":...} object) without hanging.
//
// Both programs speak spec/ABI.md: one JSON request on stdin, one JSON object on stdout.
//
// stdlib only -> builds and runs offline.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Scenario struct {
	ID          string                 `json:"id"`
	Section     string                 `json:"section"` // replay | procedural | adversarial
	Weight      float64                `json:"weight"`
	Request     map[string]interface{} `json:"request"`
	RawRequest  string                 `json:"raw_request"` // raw bytes sent verbatim (adversarial only)
	Compare     []string               `json:"compare"`
	ExpectError bool                   `json:"expect_error"`

	file string // source path, for diagnostics
}

type Result struct {
	ID      string  `json:"id"`
	Section string  `json:"section"`
	Weight  float64 `json:"weight"`
	Pass    bool    `json:"pass"`
	Detail  string  `json:"detail"`
}

var sectionWeights = map[string]float64{
	"replay":      0.65,
	"procedural":  0.25,
	"adversarial": 0.10,
}

var validSections = map[string]bool{"replay": true, "procedural": true, "adversarial": true}
var validOps = map[string]bool{"hash": true, "rom_digest": true, "unit": true}

// argList collects a repeated flag into a slice (e.g. -oracle-arg a -oracle-arg b).
type argList []string

func (a *argList) String() string  { return strings.Join(*a, " ") }
func (a *argList) Set(v string) error {
	*a = append(*a, v)
	return nil
}

// runResult captures everything the grader needs to classify a single invocation.
type runResult struct {
	parsed   map[string]interface{}
	exit     int
	timedOut bool
	jsonErr  error // stdout was non-empty but not valid JSON
	execErr  error // process could not be started / non-exit failure
}

func runCmd(bin string, args []string, input []byte, timeout time.Duration) runResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = bytes.NewReader(input)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb

	var rr runResult
	runErr := cmd.Run()
	rr.timedOut = ctx.Err() == context.DeadlineExceeded
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			rr.exit = ee.ExitCode()
		} else {
			rr.execErr = fmt.Errorf("exec %q: %v (%s)", bin, runErr, strings.TrimSpace(errb.String()))
			return rr
		}
	}
	if out.Len() > 0 {
		if jerr := json.Unmarshal(out.Bytes(), &rr.parsed); jerr != nil {
			rr.jsonErr = fmt.Errorf("invalid JSON output: %v", jerr)
		}
	}
	return rr
}

func canon(v interface{}) string { b, _ := json.Marshal(v); return string(b) }

// loadScenarios reads, parses, and validates every scenario; returns them or a list of errors.
func loadScenarios(dir string) ([]Scenario, []string) {
	var files []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".json") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)

	var scenarios []Scenario
	var errs []string
	seen := map[string]string{} // id -> file

	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: read error: %v", f, err))
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		var s Scenario
		if err := dec.Decode(&s); err != nil {
			errs = append(errs, fmt.Sprintf("%s: parse error: %v", f, err))
			continue
		}
		s.file = f
		if s.Weight == 0 {
			s.Weight = 1
		}

		// validation
		if s.ID == "" {
			errs = append(errs, fmt.Sprintf("%s: missing id", f))
		} else if prev, dup := seen[s.ID]; dup {
			errs = append(errs, fmt.Sprintf("%s: duplicate id %q (also in %s)", f, s.ID, prev))
		} else {
			seen[s.ID] = f
		}
		if !validSections[s.Section] {
			errs = append(errs, fmt.Sprintf("%s: unknown section %q", f, s.Section))
		}
		if s.Weight < 0 {
			errs = append(errs, fmt.Sprintf("%s: weight must be positive", f))
		}
		if s.ExpectError {
			hasReq := len(s.Request) > 0
			hasRaw := s.RawRequest != ""
			if hasReq == hasRaw { // both or neither
				errs = append(errs, fmt.Sprintf("%s: error scenario needs exactly one of request/raw_request", f))
			}
		} else {
			if s.RawRequest != "" {
				errs = append(errs, fmt.Sprintf("%s: raw_request only allowed with expect_error", f))
			}
			if len(s.Request) == 0 {
				errs = append(errs, fmt.Sprintf("%s: missing request", f))
			} else if op, _ := s.Request["op"].(string); !validOps[op] {
				errs = append(errs, fmt.Sprintf("%s: invalid request.op %q", f, op))
			}
			if len(s.Compare) == 0 {
				errs = append(errs, fmt.Sprintf("%s: non-error scenario needs a non-empty compare", f))
			}
		}
		scenarios = append(scenarios, s)
	}
	if len(scenarios) == 0 && len(errs) == 0 {
		errs = append(errs, fmt.Sprintf("no scenarios found under %s", dir))
	}
	return scenarios, errs
}

func (s Scenario) inputBytes() []byte {
	if s.RawRequest != "" {
		return []byte(s.RawRequest)
	}
	b, _ := json.Marshal(s.Request)
	return b
}

// gradeError handles an expect_error scenario, thus the candidate must reject the input.
func gradeError(s Scenario, agentBin string, agentArgs []string, timeout time.Duration) Result {
	res := Result{ID: s.ID, Section: s.Section, Weight: s.Weight}
	rr := runCmd(agentBin, agentArgs, s.inputBytes(), timeout)
	switch {
	case rr.timedOut:
		res.Pass = false
		res.Detail = "candidate hung on invalid input (timeout)"
	case rr.execErr != nil:
		// could not even start the process; that is not a clean rejection
		res.Pass = false
		res.Detail = "candidate not runnable: " + rr.execErr.Error()
	case rr.exit != 0:
		res.Pass = true
		res.Detail = fmt.Sprintf("correctly rejected invalid input (exit %d)", rr.exit)
	case rr.parsed != nil && rr.parsed["error"] != nil:
		res.Pass = true
		res.Detail = "correctly rejected invalid input ({\"error\":…})"
	default:
		res.Pass = false
		res.Detail = "accepted input that should be rejected (exit 0, no error)"
	}
	return res
}

// gradeMatch handles a normal scenario: candidate output must match the oracle on Compare fields.
func gradeMatch(s Scenario, oracleBin string, oracleArgs []string, agentBin string, agentArgs []string, timeout time.Duration) Result {
	res := Result{ID: s.ID, Section: s.Section, Weight: s.Weight}
	in := s.inputBytes()

	exp := runCmd(oracleBin, oracleArgs, in, timeout)
	if exp.timedOut || exp.execErr != nil || exp.jsonErr != nil || exp.parsed == nil || exp.exit != 0 {
		res.Pass = false
		res.Detail = "oracle failed: " + oracleDetail(exp)
		return res
	}

	act := runCmd(agentBin, agentArgs, in, timeout)
	switch {
	case act.timedOut:
		res.Detail = "candidate timed out"
		return res
	case act.execErr != nil:
		res.Detail = "candidate not runnable: " + act.execErr.Error()
		return res
	case act.exit != 0:
		res.Detail = fmt.Sprintf("candidate rejected valid input (exit %d)", act.exit)
		return res
	case act.jsonErr != nil:
		res.Detail = "candidate produced " + act.jsonErr.Error()
		return res
	case act.parsed == nil:
		res.Detail = "candidate produced no output"
		return res
	}

	for _, field := range s.Compare {
		ev, ok := exp.parsed[field]
		if !ok {
			res.Detail = "oracle output missing field " + field
			return res
		}
		av, ok := act.parsed[field]
		if !ok {
			res.Detail = "candidate output missing field " + field
			return res
		}
		if canon(ev) != canon(av) {
			res.Detail = fmt.Sprintf("mismatch on %s", field)
			return res
		}
	}
	res.Pass = true
	res.Detail = "match: " + strings.Join(s.Compare, ",")
	return res
}

func oracleDetail(rr runResult) string {
	switch {
	case rr.timedOut:
		return "timed out"
	case rr.execErr != nil:
		return rr.execErr.Error()
	case rr.jsonErr != nil:
		return rr.jsonErr.Error()
	case rr.exit != 0:
		return fmt.Sprintf("non-zero exit %d", rr.exit)
	case rr.parsed == nil:
		return "no output"
	}
	return "unknown"
}

func main() {
	agentBin := flag.String("agent-bin", "", "candidate executable (reads JSON stdin, writes JSON stdout)")
	oracleBin := flag.String("oracle-bin", "oracle", "reference oracle executable (same ABI)")
	var agentArgs, oracleArgs argList
	flag.Var(&agentArgs, "agent-arg", "argument for the candidate executable (repeatable)")
	flag.Var(&oracleArgs, "oracle-arg", "argument for the oracle executable (repeatable)")
	scenDir := flag.String("scenarios", "scenarios", "scenarios directory")
	outPath := flag.String("out", "scorecard.json", "scorecard output path")
	timeout := flag.Duration("timeout", 120*time.Second, "per-run timeout")
	flag.Parse()

	if *agentBin == "" {
		fmt.Fprintln(os.Stderr, "error: -agent-bin is required")
		os.Exit(2)
	}

	scenarios, errs := loadScenarios(*scenDir)
	if len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "scenario validation failed:")
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "  - "+e)
		}
		os.Exit(2)
	}

	var results []Result
	for _, s := range scenarios {
		if s.ExpectError {
			results = append(results, gradeError(s, *agentBin, agentArgs, *timeout))
		} else {
			results = append(results, gradeMatch(s, *oracleBin, oracleArgs, *agentBin, agentArgs, *timeout))
		}
	}

	type acc struct{ num, den float64 }
	secAcc := map[string]*acc{}
	for _, r := range results {
		a := secAcc[r.Section]
		if a == nil {
			a = &acc{}
			secAcc[r.Section] = a
		}
		a.den += r.Weight
		if r.Pass {
			a.num += r.Weight
		}
	}
	sectionScores := map[string]float64{}
	for sec, a := range secAcc {
		if a.den > 0 {
			sectionScores[sec] = a.num / a.den
		}
	}
	overall, wsum := 0.0, 0.0
	for sec, w := range sectionWeights {
		if sc, ok := sectionScores[sec]; ok {
			overall += w * sc
			wsum += w
		}
	}
	if wsum > 0 {
		overall /= wsum
	}

	card := map[string]interface{}{
		"overall":         overall,
		"section_scores":  sectionScores,
		"section_weights": sectionWeights,
		"results":         results,
		"generated_at":    time.Now().UTC().Format(time.RFC3339),
	}
	b, _ := json.MarshalIndent(card, "", "  ")
	_ = os.WriteFile(*outPath, b, 0o644)

	fmt.Printf("overall: %.1f%%\n", overall*100)
	secs := make([]string, 0, len(sectionScores))
	for s := range sectionScores {
		secs = append(secs, s)
	}
	sort.Strings(secs)
	for _, s := range secs {
		fmt.Printf("  %-12s %.1f%%\n", s, sectionScores[s]*100)
	}
	fmt.Printf("scorecard: %s\n", *outPath)
}
