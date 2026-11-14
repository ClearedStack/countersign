package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var at = time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)

type result struct {
	code   int
	stdout string
	stderr string
}

// cs runs the command line in this process, with a fixed clock.
func cs(t *testing.T, spool, stdin string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	e := env{stdin: strings.NewReader(stdin), stdout: &out, stderr: &errb, now: func() time.Time { return at }}
	code := run(append([]string{"-spool", spool}, args...), e)
	return result{code, out.String(), errb.String()}
}

const (
	scoredItem   = `{"id":"PA-0001","kind":"item","state":"scored","lane":"normal","title":"Check a sample labor rate against a made-up ceiling","score":82,"recommendation":"approved"}`
	screenedItem = `{"id":"PA-0002","kind":"item","state":"screened_out","lane":"normal","title":"Draft, price and submit a whole sample bid","screen_reason":"one_job: It does three jobs, not one."}`
	proposalItem = `{"id":"EP-0001","kind":"proposal","state":"pending_approval","lane":"urgent","target":"catalog/sample-rate-checker/description","repository":"clearedstack.com","file":"src/data/sample-catalog.json","edit_kind":"DATA","change":"The description says 'calender'.\nIt should say 'calendar'.","reason":"A spelling mistake."}`
)

func TestOverrideIsRefusedWithoutAWrittenReason(t *testing.T) {
	spool := t.TempDir()
	for _, it := range []string{scoredItem, screenedItem, proposalItem} {
		if r := cs(t, spool, it, "add"); r.code != exitOK {
			t.Fatalf("add: %+v", r)
		}
	}

	r := cs(t, spool, "", "list")
	wantList := `ID       KIND      STATE             LANE    DETAIL                                        SUMMARY
EP-0001  proposal  pending_approval  urgent  DATA catalog/sample-rate-checker/description  The description says 'calender'. It should say 'calendar'.
PA-0001  item      scored            normal  score 82, recommended approved                Check a sample labor rate against a made-up ceiling
PA-0002  item      screened_out      normal  one_job: It does three jobs, not one.         Draft, price and submit a whole sample bid
`
	if r.code != exitOK || r.stdout != wantList {
		t.Errorf("list:\n%s\nwant:\n%s", r.stdout, wantList)
	}

	// No reason: refused, and nothing moves.
	for _, args := range [][]string{
		{"decide", "PA-0002", "override"},
		{"decide", "PA-0002", "override", "-reason", "   "},
		{"decide", "-reason", "", "PA-0002", "override"},
	} {
		r = cs(t, spool, "", args...)
		want := "E_PARSE decision.override_reason_missing PA-0002: an override needs a written reason [fp 67665956733e504d]\n"
		if r.code != exitError || r.stderr != want || r.stdout != "" {
			t.Errorf("%v: %+v", args, r)
		}
	}
	if r = cs(t, spool, "", "log"); r.stdout != "" {
		t.Errorf("a refused override was recorded: %s", r.stdout)
	}
	if r = cs(t, spool, "", "show", "PA-0002"); r.code != exitOK || !strings.Contains(r.stdout, `"override"`) {
		t.Errorf("the item is no longer waiting: %+v", r)
	}

	// With a reason, in either flag position.
	r = cs(t, spool, "", "decide", "PA-0002", "-reason", "Reshaped to do one job.", "override")
	if r.code != exitOK {
		t.Fatalf("override with reason: %+v", r)
	}
	var res struct {
		Rule   string
		Record struct {
			Outcome, From, To, Reason, At string
			Override                      bool
		}
	}
	if err := json.Unmarshal([]byte(r.stdout), &res); err != nil {
		t.Fatal(err)
	}
	if res.Rule != "decision.override" || res.Record.From != "screened_out" || res.Record.To != "observed" ||
		!res.Record.Override || res.Record.Reason != "Reshaped to do one job." || res.Record.At != "2026-01-05T10:00:00Z" {
		t.Errorf("record: %+v", res)
	}

	// The item is gone from the inbox and cannot be decided twice.
	if r = cs(t, spool, "", "decide", "PA-0002", "override", "-reason", "Again."); r.code != exitError || !strings.HasPrefix(r.stderr, "E_PARSE decision.item_unknown PA-0002:") {
		t.Errorf("second decision: %+v", r)
	}
	// Choosing against the recommendation is an override too.
	if r = cs(t, spool, "", "decide", "PA-0001", "park", "-blocker", "No capacity."); r.code != exitError || !strings.HasPrefix(r.stderr, "E_PARSE decision.override_reason_missing PA-0001:") {
		t.Errorf("park against recommendation: %+v", r)
	}
	if r = cs(t, spool, "", "decide", "PA-0001", "approve"); r.code != exitOK {
		t.Errorf("approve as recommended: %+v", r)
	}
	if r = cs(t, spool, `{"item":"EP-0001","outcome":"decline"}`, "decide", "-f", "-"); r.code != exitOK {
		t.Errorf("decide -f: %+v", r)
	}

	r = cs(t, spool, "", "log")
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("log:\n%s", r.stdout)
	}
	for i, item := range []string{"PA-0002", "PA-0001", "EP-0001"} {
		if !strings.Contains(lines[i], fmt.Sprintf(`"seq":%d,"status":"waiting"`, i+1)) || !strings.Contains(lines[i], `"item":"`+item+`"`) {
			t.Errorf("log line %d: %s", i+1, lines[i])
		}
	}
	if r = cs(t, spool, "", "list"); strings.Count(r.stdout, "\n") != 1 {
		t.Errorf("the inbox is not empty:\n%s", r.stdout)
	}
}

func TestBrokenAndUnfamiliarItems(t *testing.T) {
	spool := t.TempDir()
	r := cs(t, spool, "not json", "add")
	if r.code != exitError || !strings.HasPrefix(r.stderr, "E_PARSE intake.malformed:") {
		t.Errorf("broken: %+v", r)
	}
	r = cs(t, spool, strings.Replace(scoredItem, `"item"`, `"bundle"`, 1), "add")
	if r.code != exitError || !strings.HasPrefix(r.stderr, "E_NEEDS_SUBTOOL intake.kind_unfamiliar PA-0001:") {
		t.Errorf("unfamiliar: %+v", r)
	}
	r = cs(t, spool, strings.Replace(scoredItem, `"PA-0001"`, `"PA-01"`, 1), "add")
	if r.code != exitError || !strings.HasPrefix(r.stderr, "E_PARSE intake.id_invalid PA-01:") {
		t.Errorf("identifier: %+v", r)
	}
	r = cs(t, spool, strings.Repeat("x", 40<<10), "add")
	if r.code != exitError || !strings.HasPrefix(r.stderr, "E_PARSE intake.payload_too_large:") {
		t.Errorf("too large: %+v", r)
	}
	// Nothing reached the inbox, and with no lease there is nothing to tell
	// Muster.
	if r = cs(t, spool, "", "log"); r.stdout != "" {
		t.Errorf("log: %s", r.stdout)
	}
	if r = cs(t, spool, "", "list"); strings.Count(r.stdout, "\n") != 1 {
		t.Errorf("list: %s", r.stdout)
	}
}

func TestUsage(t *testing.T) {
	spool := t.TempDir()
	for _, args := range [][]string{
		{},
		{"frobnicate"},
		{"show"},
		{"decide", "PA-0001"},
		{"decide", "PA-0001", "approve", "extra"},
		{"decide", "PA-0001", "approve", "-force"},
		{"decide", "-f", "-", "PA-0001", "approve"},
		{"add", "a", "b"},
		{"add", filepath.Join(spool, "no-such-file")},
		{"run"},
		{"run", "-standin", filepath.Join(spool, "no-such-file")},
		{"run", "-standin", "x", "-muster", "http://muster.invalid"},
		{"run", "-muster", "muster.invalid:8080"},
		{"run", "-muster", "http://user:made-up@muster.invalid"},
	} {
		r := cs(t, spool, "", args...)
		if r.code != exitUsage || r.stdout != "" {
			t.Errorf("%v: %+v", args, r)
		}
		if strings.Contains(r.stderr, spool) {
			t.Errorf("%v: the message names a path: %s", args, r.stderr)
		}
	}
	if r := cs(t, spool, "", "version"); r.stdout != "countersign v0.1.0-draft\n" {
		t.Errorf("version: %+v", r)
	}
	if r := cs(t, spool, "", "rules"); r.code != exitOK || !strings.Contains(r.stdout, "decision.override_reason_missing  E_PARSE") {
		t.Errorf("rules: %+v", r)
	}
}

func TestVersionMatchesTheRegistryEntry(t *testing.T) {
	b, err := os.ReadFile("../../component.json")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Slug, Version string
		SubTools      []string
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if c.Version != Version || c.Slug != "countersign" || len(c.SubTools) != 0 {
		t.Errorf("component.json says %+v, the program says %s", c, Version)
	}
}

func TestStoreErrorNamesNoPath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "private-volume")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := cs(t, blocker, "", "list")
	if r.code != exitError || !strings.HasPrefix(r.stderr, "E_TIMEOUT store.unavailable:") || strings.Contains(r.stderr, "private-volume") {
		t.Errorf("%+v", r)
	}
}

func TestExampleSeedRuns(t *testing.T) {
	spool := t.TempDir()
	r := cs(t, spool, "", "run", "-standin", "../../examples/standin.json", "-interval", "1ms", "-ticks", "3")
	if r.code != exitOK {
		t.Fatalf("%+v", r)
	}
	for _, want := range []string{
		`"rule":"lease.timeout","code":"E_TIMEOUT"`,
		`"rule":"intake.accept","item":"PA-0001"`,
		`"rule":"intake.accept","item":"PA-0002"`,
		`"rule":"intake.recommendation_unfamiliar","code":"E_NEEDS_SUBTOOL","item":"PA-0004"`,
		`"rule":"spool.refused","code":"E_PARSE","item":"PA-0004","seq":1`,
	} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("the worker log lacks %s:\n%s", want, r.stderr)
		}
	}
	if r = cs(t, spool, "", "list"); strings.Count(r.stdout, "\n") != 5 {
		t.Errorf("list:\n%s", r.stdout)
	}
}

func TestHeldAndRelease(t *testing.T) {
	spool := t.TempDir()
	seed := filepath.Join(t.TempDir(), "seed.json")
	// One item Assay scored without a total: broken, so Countersign stops it.
	if err := os.WriteFile(seed, []byte(`{"steps":[
		{"kind":"item","id":"PA-0001","state":"scored","lane":"normal","results":{"assay":{"recommendation":"build"}}},
		{"kind":"item","id":"PA-0002","state":"scored","lane":"normal","results":{"assay":{"score":82,"recommendation":"build"}}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := cs(t, spool, "", "run", "-standin", seed, "-interval", "1ms", "-ticks", "2"); r.code != exitOK ||
		!strings.Contains(r.stderr, `"rule":"intake.field_missing","code":"E_PARSE","item":"PA-0001"`) ||
		!strings.Contains(r.stderr, `"rule":"spool.acknowledged","item":"PA-0001","seq":1`) {
		t.Fatalf("run: %+v", r)
	}
	r := cs(t, spool, "", "held")
	want := "ID       STOPPED               RULE\nPA-0001  2026-01-05T10:00:00Z  intake.field_missing\n"
	if r.code != exitOK || r.stdout != want {
		t.Errorf("held:\n%s\nwant:\n%s", r.stdout, want)
	}

	for _, args := range [][]string{{"release", "EP-0001"}, {"release", "PA-01"}, {"release", "../x"}} {
		if r := cs(t, spool, "", args...); r.code != exitError || !strings.HasPrefix(r.stderr, "E_PARSE decision.malformed") {
			t.Errorf("%v: %+v", args, r)
		}
	}
	for _, args := range [][]string{{"release"}, {"release", "PA-0001", "PA-0002"}, {"release", "PA-0001", "-force"}} {
		if r := cs(t, spool, "", args...); r.code != exitUsage {
			t.Errorf("%v: %+v", args, r)
		}
	}
	r = cs(t, spool, "", "release", "PA-0001", "-reason", "Assay's result was corrected.")
	if r.code != exitOK || !strings.Contains(r.stdout, `"rule": "decision.release"`) || !strings.Contains(r.stdout, `"call": "release"`) {
		t.Fatalf("release: %+v", r)
	}
	// A release that is waiting to be sent takes the item off the list.
	if r = cs(t, spool, "", "held"); r.stdout != "ID  STOPPED  RULE\n" {
		t.Errorf("held after the release: %q", r.stdout)
	}
	if r = cs(t, spool, "", "log"); strings.Count(r.stdout, "\n") != 2 || !strings.Contains(r.stdout, `"seq":2,"status":"waiting","record":{"contract":"v1","call":"release"`) {
		t.Errorf("log:\n%s", r.stdout)
	}
}

func TestEnvironmentVariables(t *testing.T) {
	spool := t.TempDir()
	runEnv := func(vars map[string]string, args ...string) result {
		t.Helper()
		var out, errb bytes.Buffer
		e := env{
			stdin:  strings.NewReader(""),
			stdout: &out,
			stderr: &errb,
			now:    func() time.Time { return at },
			getenv: func(k string) string { return vars[k] },
		}
		return result{run(args, e), out.String(), errb.String()}
	}

	// COUNTERSIGN_SPOOL selects the spool when -spool is not given.
	var out, errb bytes.Buffer
	code := run([]string{"add"}, env{
		stdin:  strings.NewReader(scoredItem),
		stdout: &out,
		stderr: &errb,
		now:    func() time.Time { return at },
		getenv: func(k string) string {
			if k == "COUNTERSIGN_SPOOL" {
				return spool
			}
			return ""
		},
	})
	if code != exitOK {
		t.Fatalf("add via COUNTERSIGN_SPOOL: %d %s", code, errb.String())
	}
	if r := runEnv(map[string]string{"COUNTERSIGN_SPOOL": spool}, "list"); r.code != exitOK || !strings.Contains(r.stdout, "PA-0001") {
		t.Errorf("list via COUNTERSIGN_SPOOL: %+v", r)
	}

	// CONTRACT_VERSION other than v1 is refused without echoing its value.
	r := runEnv(map[string]string{
		"COUNTERSIGN_SPOOL": spool,
		"MUSTER_URL":        "http://127.0.0.1:1",
		"CONTRACT_VERSION":  "secret-v2",
	}, "run", "-ticks", "1")
	if r.code != exitUsage || strings.Contains(r.stderr, "secret-v2") {
		t.Errorf("bad CONTRACT_VERSION: %+v", r)
	}

	// MUSTER_URL + CONTRACT_VERSION=v1 runs one tick (unreachable Muster is E_TIMEOUT, exit 0).
	r = runEnv(map[string]string{
		"COUNTERSIGN_SPOOL": spool,
		"MUSTER_URL":        "http://127.0.0.1:1",
		"CONTRACT_VERSION":  "v1",
	}, "run", "-interval", "1ms", "-ticks", "1")
	if r.code != exitOK || !strings.Contains(r.stderr, `"code":"E_TIMEOUT"`) {
		t.Errorf("run via MUSTER_URL: %+v", r)
	}

	// Explicit -standin still works when MUSTER_URL is set in the environment.
	r = runEnv(map[string]string{
		"COUNTERSIGN_SPOOL": t.TempDir(),
		"MUSTER_URL":        "http://127.0.0.1:1",
	}, "run", "-standin", "../../examples/standin.json", "-interval", "1ms", "-ticks", "1")
	if r.code != exitOK {
		t.Errorf("run -standin with MUSTER_URL set: %+v", r)
	}
}

func TestReviewInteractiveWorkflow(t *testing.T) {
	spool := t.TempDir()
	if r := cs(t, spool, "", "review"); r.code != exitOK || r.stdout != "no items waiting.\n" {
		t.Fatalf("empty review: %+v", r)
	}

	for _, it := range []string{scoredItem, screenedItem, proposalItem} {
		if r := cs(t, spool, it, "add"); r.code != exitOK {
			t.Fatalf("add: %+v", r)
		}
	}

	// Step through EP-0001 (approve), PA-0001 (bad outcome, disallowed outcome,
	// override missing reason, then valid park override), and PA-0002 (blank -> skip).
	session := strings.Join([]string{
		"approve",
		"",
		"maybe",
		"override",
		"park",
		"Waiting on FAR update",
		"   ",
		"park",
		"Waiting on FAR update",
		"Hold until Q3 rule text lands.",
		"",
	}, "\n") + "\n"

	r := cs(t, spool, session, "review")
	if r.code != exitOK {
		t.Fatalf("review failed: %+v", r)
	}
	for _, wantOut := range []string{
		"[1/3] EP-0001  proposal  pending_approval  lane=urgent",
		"recorded EP-0001: pending_approval -> approved (decision.proposal_approve)",
		"[2/3] PA-0001  item  scored  lane=normal",
		"recorded PA-0001: scored -> parked (decision.park) [override]",
		"[3/3] PA-0002  item  screened_out  lane=normal",
		"skipped PA-0002.",
	} {
		if !strings.Contains(r.stdout, wantOut) {
			t.Errorf("review stdout missing %q:\n%s", wantOut, r.stdout)
		}
	}
	for _, wantErr := range []string{
		"E_PARSE decision.outcome_unknown PA-0001:",
		"E_PARSE decision.outcome_not_allowed PA-0001:",
		"E_PARSE decision.override_reason_missing PA-0001:",
	} {
		if !strings.Contains(r.stderr, wantErr) {
			t.Errorf("review stderr missing %q:\n%s", wantErr, r.stderr)
		}
	}

	// PA-0002 was skipped and must still be waiting; EOF mid-prompt must not record it.
	r = cs(t, spool, "override\n", "review", "PA-0002")
	if r.code != exitOK {
		t.Fatalf("review EOF mid-item: %+v", r)
	}
	if r = cs(t, spool, "", "show", "PA-0002"); r.code != exitOK {
		t.Fatalf("PA-0002 disappeared on EOF: %+v", r)
	}

	// CRLF line endings work cleanly when reviewing by explicit ID.
	r = cs(t, spool, "override\r\nReshaped to do one job.\r\n", "review", "PA-0002")
	if r.code != exitOK || !strings.Contains(r.stdout, "recorded PA-0002: screened_out -> observed (decision.override) [override]") {
		t.Fatalf("review CRLF override: %+v", r)
	}
	if r = cs(t, spool, "", "review"); r.code != exitOK || r.stdout != "no items waiting.\n" {
		t.Errorf("inbox should now be empty: %+v", r)
	}
}

func TestReviewFiltersOutcomesAndBounds(t *testing.T) {
	spool := t.TempDir()
	items := []string{
		`{"id":"PA-0010","kind":"item","state":"scored","lane":"urgent","title":"Link sample","score":65,"recommendation":"linked","split_flag":true}`,
		`{"id":"PA-0011","kind":"item","state":"scored","lane":"normal","title":"Merge sample","score":70,"recommendation":"approved"}`,
		`{"id":"PA-0012","kind":"item","state":"scored","lane":"backfill","title":"Reject sample","score":30,"recommendation":"rejected"}`,
		proposalItem,
	}
	for _, it := range items {
		if r := cs(t, spool, it, "add"); r.code != exitOK {
			t.Fatalf("add: %+v", r)
		}
	}

	// Usage and unknown-item checks.
	for _, bad := range [][]string{
		{"review", "-bogus"},
		{"review", "-kind", "bundle"},
		{"review", "-lane", "fast"},
		{"review", "-kind", "item", "PA-0010"},
	} {
		if r := cs(t, spool, "", bad...); r.code != exitUsage {
			t.Errorf("%v: want exitUsage, got %+v", bad, r)
		}
	}
	if r := cs(t, spool, "", "review", "PA-9999"); r.code != exitError || !strings.HasPrefix(r.stderr, "E_PARSE decision.item_unknown PA-9999:") {
		t.Errorf("unknown id: %+v", r)
	}

	// Filter to urgent candidates only: sees PA-0010 (with split_flag), links it.
	r := cs(t, spool, "link\nsample-existing-tool\n\n", "review", "-kind", "item", "-lane", "urgent")
	if r.code != exitOK ||
		!strings.Contains(r.stdout, "split_flag: true") ||
		!strings.Contains(r.stdout, "recorded PA-0010: scored -> linked (decision.link)") {
		t.Fatalf("urgent item review: %+v", r)
	}

	// Overlong input lines are bounded, refused with E_PARSE, and allow quit.
	huge := strings.Repeat("a", 40<<10)
	r = cs(t, spool, huge+"\nmerge\n"+huge+"\nreason\nquit\n", "review", "-kind", "item")
	if r.code != exitOK ||
		!strings.Contains(r.stderr, "E_PARSE decision.outcome_unknown PA-0011:") ||
		!strings.Contains(r.stderr, "E_PARSE decision.text_invalid PA-0011:") {
		t.Fatalf("overlong input handling: %+v", r)
	}

	// Complete PA-0011 (merge) and PA-0012 (reject) in one pass.
	r = cs(t, spool, "merge\nsample-parent\nFold into parent calculator.\nreject\nLow score and duplicate scope.\n", "review", "-kind", "item")
	if r.code != exitOK ||
		!strings.Contains(r.stdout, "recorded PA-0011: scored -> merged (decision.merge) [override]") ||
		!strings.Contains(r.stdout, "recorded PA-0012: scored -> rejected (decision.reject)") {
		t.Fatalf("merge and reject review: %+v", r)
	}

	// Proposal EP-0001 was untouched by -kind item; decline it via -kind proposal.
	r = cs(t, spool, "decline\n\n", "review", "-kind", "proposal")
	if r.code != exitOK || !strings.Contains(r.stdout, "recorded EP-0001: pending_approval -> declined (decision.proposal_decline)") {
		t.Fatalf("proposal decline review: %+v", r)
	}
}
