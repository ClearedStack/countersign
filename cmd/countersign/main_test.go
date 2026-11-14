package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/muster"
	"github.com/ClearedStack/countersign/internal/store"
	"github.com/ClearedStack/countersign/internal/worker"
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

func TestStatusSummaryAndJSON(t *testing.T) {
	spool := t.TempDir()

	// Usage checks.
	for _, bad := range [][]string{{"status", "extra"}, {"status", "-bogus"}} {
		if r := cs(t, spool, "", bad...); r.code != exitUsage {
			t.Errorf("%v: want exitUsage, got %+v", bad, r)
		}
	}

	// Empty spool.
	r := cs(t, spool, "", "status")
	wantEmpty := `INBOX   0 waiting  (urgent 0, normal 0, backfill 0; scored 0, screened_out 0, proposals 0)
OUTBOX  0 queued   (waiting 0, unleased 0)
HELD    0 stopped  (local E_PARSE awaiting release)
LOG     0 settled  (acknowledged 0, refused 0)
`
	if r.code != exitOK || r.stdout != wantEmpty {
		t.Fatalf("empty status:\n%s\nwant:\n%s", r.stdout, wantEmpty)
	}

	// Run stand-in seed (PA-0001 scored normal, PA-0002 screened_out normal,
	// PA-0004 unfamiliar -> refused pause, plus proposals in examples/standin.json),
	// plus one broken item stopped with E_PARSE.
	seed := filepath.Join(t.TempDir(), "seed.json")
	if err := os.WriteFile(seed, []byte(`{"steps":[
		{"kind":"item","id":"PA-0001","state":"scored","lane":"normal","results":{"assay":{"score":82,"recommendation":"build"}}},
		{"kind":"item","id":"PA-0002","state":"screened_out","lane":"backfill","results":{"assay":{"gates":{"one_job":{"passed":false,"reason":"Too broad."}}}}},
		{"kind":"item","id":"PA-0003","state":"scored","lane":"normal","results":{"assay":{"recommendation":"build"}}},
		{"kind":"item","id":"PA-0004","state":"scored","lane":"normal","results":{"assay":{"score":70,"recommendation":"split"}}},
		{"kind":"proposal","id":"EP-0001","state":"pending_approval","lane":"urgent","proposal":{"target":"catalog/x/desc","edit_kind":"DATA","change":"Fix typo."}}
	],"ownership":["catalog/x/desc"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if r = cs(t, spool, "", "run", "-standin", seed, "-interval", "1ms", "-ticks", "2"); r.code != exitOK {
		t.Fatalf("run seed: %+v", r)
	}
	// Decide EP-0001 so one record sits waiting in the outbox.
	if r = cs(t, spool, "", "decide", "EP-0001", "approve"); r.code != exitOK {
		t.Fatalf("decide EP-0001: %+v", r)
	}

	r = cs(t, spool, "", "status")
	wantPopulated := `INBOX   2 waiting  (urgent 0, normal 1, backfill 1; scored 1, screened_out 1, proposals 0)
OUTBOX  1 queued   (waiting 1, unleased 0)
HELD    1 stopped  (local E_PARSE awaiting release)
LOG     2 settled  (acknowledged 1, refused 1)
`
	if r.code != exitOK || r.stdout != wantPopulated {
		t.Errorf("status:\n%s\nwant:\n%s", r.stdout, wantPopulated)
	}

	r = cs(t, spool, "", "status", "-json")
	if r.code != exitOK {
		t.Fatalf("status -json: %+v", r)
	}
	var got statusSummary
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("unmarshal status -json: %v\n%s", err, r.stdout)
	}
	if got.Inbox.Total != 2 || got.Inbox.Normal != 1 || got.Inbox.Backfill != 1 ||
		got.Inbox.Scored != 1 || got.Inbox.ScreenedOut != 1 || got.Inbox.Proposals != 0 ||
		got.Outbox.Waiting != 1 || got.Held != 1 || got.Log.Acknowledged != 1 || got.Log.Refused != 1 {
		t.Errorf("unexpected status JSON: %+v", got)
	}
}

// calculatorSpec is Waybill's own accepted example, copied from
// tools/spec-profiles/calculator/testdata/ok-capped-allowance/record.json in
// ClearedStack/waybill, branch consort/ready-to-deploy (commit 52c51a9), with
// the two edits Waybill's worker test makes to it before playing it as
// results.countersign: item_id and state left out, and an official https url
// on the authority.
const calculatorSpec = "../../testdata/waybill/ok-capped-allowance.json"

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readSpec(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(calculatorSpec)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// paddedSpec returns the calculator specification, otherwise valid, padded in
// its summary so that the file is exactly n bytes long.
func paddedSpec(t *testing.T, n int) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(readSpec(t)), &m); err != nil {
		t.Fatal(err)
	}
	m["summary"] = ""
	base, err := json.Marshal(m)
	if err != nil || n < len(base) {
		t.Fatalf("cannot pad to %d bytes: %v", n, err)
	}
	m["summary"] = strings.Repeat("a", n-len(base))
	out, err := json.Marshal(m)
	if err != nil || len(out) != n {
		t.Fatalf("padded to %d bytes, want %d: %v", len(out), n, err)
	}
	return string(out)
}

func TestReadWaybillHoldsTheSizeLimit(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		path string
		ok   bool
	}{
		{"exactly the limit", writeFile(t, paddedSpec(t, item.MaxWaybill)), true},
		{"one byte over the limit", writeFile(t, paddedSpec(t, item.MaxWaybill+1)), false},
		{"empty", writeFile(t, ""), false},
		{"a directory", dir, false},
		{"missing", filepath.Join(dir, "none.json"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, ok := readWaybill(tt.path)
			if ok != tt.ok || (ok && len(b) != item.MaxWaybill) {
				t.Fatalf("readWaybill = %d bytes, %v; want ok=%v", len(b), ok, tt.ok)
			}
		})
	}
}

func addAll(t *testing.T, spool string) {
	t.Helper()
	for _, it := range []string{scoredItem, screenedItem, proposalItem} {
		if r := cs(t, spool, it, "add"); r.code != exitOK {
			t.Fatalf("add: %+v", r)
		}
	}
}

func TestDecideWaybillIsRefusedAndNothingIsSpooled(t *testing.T) {
	spec := readSpec(t)
	good := writeFile(t, spec)
	dup := writeFile(t, strings.Replace(spec, `"title"`, `"tool_type": "calculator", "title"`, 1))
	big := writeFile(t, paddedSpec(t, item.MaxWaybill+1))

	tests := []struct {
		name string
		args []string
		rule string
	}{
		{"missing file", []string{"decide", "PA-0001", "approve", "-waybill", filepath.Join(t.TempDir(), "none.json")}, "decision.malformed"},
		{"a directory", []string{"decide", "PA-0001", "approve", "-waybill", t.TempDir()}, "decision.malformed"},
		{"empty file", []string{"decide", "PA-0001", "approve", "-waybill", writeFile(t, "")}, "decision.malformed"},
		{"oversized file", []string{"decide", "PA-0001", "approve", "-waybill", big}, "decision.malformed"},
		{"not json", []string{"decide", "PA-0001", "approve", "-waybill", writeFile(t, `{"title":`)}, "decision.malformed"},
		{"an array", []string{"decide", "PA-0001", "approve", "-waybill", writeFile(t, `[]`)}, "decision.malformed"},
		{"duplicate key", []string{"decide", "PA-0001", "approve", "-waybill", dup}, "decision.malformed"},
		{"unknown field", []string{"decide", "PA-0001", "approve", "-waybill", writeFile(t, `{"price":1}`)}, "decision.malformed"},
		{"no title", []string{"decide", "PA-0001", "approve", "-waybill", writeFile(t, strings.Replace(spec, `"title": "prorated daily allowance with a cap",`, "", 1))}, "decision.malformed"},
		{"link", []string{"decide", "PA-0001", "link", "-tool", "t", "-reason", "r", "-waybill", good}, "decision.field_unexpected"},
		{"merge", []string{"decide", "PA-0001", "merge", "-parent", "p", "-reason", "r", "-waybill", good}, "decision.field_unexpected"},
		{"park", []string{"decide", "PA-0001", "park", "-blocker", "b", "-reason", "r", "-waybill", good}, "decision.field_unexpected"},
		{"reject", []string{"decide", "PA-0001", "reject", "-reason", "r", "-waybill", good}, "decision.field_unexpected"},
		{"approve an edit proposal", []string{"decide", "EP-0001", "approve", "-waybill", good}, "decision.field_unexpected"},
		{"decline an edit proposal", []string{"decide", "EP-0001", "decline", "-waybill", good}, "decision.field_unexpected"},
		{"outcome not allowed for the state", []string{"decide", "PA-0002", "approve", "-waybill", good}, "decision.outcome_not_allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spool := t.TempDir()
			addAll(t, spool)
			r := cs(t, spool, "", tt.args...)
			if r.code != exitError || r.stdout != "" || !strings.HasPrefix(r.stderr, "E_PARSE "+tt.rule+" ") {
				t.Fatalf("got %+v, want a refusal under %s", r, tt.rule)
			}
			if strings.Count(r.stderr, "\n") != 1 || strings.Contains(r.stderr, string(filepath.Separator)) || strings.Contains(r.stderr, "prorated") {
				t.Errorf("the refusal is not one fixed line: %q", r.stderr)
			}
			if l := cs(t, spool, "", "log"); l.stdout != "" {
				t.Errorf("a refused decision was spooled: %s", l.stdout)
			}
			if l := cs(t, spool, "", "list"); strings.Count(l.stdout, "\n") != 4 {
				t.Errorf("an item left the inbox:\n%s", l.stdout)
			}
		})
	}
}

func TestDecideWaybillCannotComeFromADecisionFile(t *testing.T) {
	spool := t.TempDir()
	addAll(t, spool)
	good := writeFile(t, readSpec(t))
	r := cs(t, spool, `{"item":"PA-0001","outcome":"approve"}`, "decide", "-f", "-", "-waybill", good)
	if r.code != exitUsage || r.stdout != "" {
		t.Errorf("decide -f with -waybill: %+v", r)
	}
	r = cs(t, spool, `{"item":"PA-0001","outcome":"approve","waybill":{"title":"t"}}`, "decide", "-f", "-")
	if r.code != exitError || !strings.HasPrefix(r.stderr, "E_PARSE decision.malformed") {
		t.Errorf("a waybill inside a decision file: %+v", r)
	}
	if l := cs(t, spool, "", "log"); l.stdout != "" {
		t.Errorf("something was spooled: %s", l.stdout)
	}
}

func TestDecideWaybillIsSpooledWithTheRecord(t *testing.T) {
	spec := readSpec(t)
	var want bytes.Buffer
	if err := json.Compact(&want, []byte(spec)); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"decide", "PA-0001", "approve", "-waybill", writeFile(t, spec)},
		{"decide", "-waybill", writeFile(t, spec), "PA-0001", "approve"},
		{"decide", "PA-0001", "approve", "--waybill", writeFile(t, spec)},
		{"decide", "PA-0001", "approve", "--waybill=" + writeFile(t, spec)},
	} {
		spool := t.TempDir()
		addAll(t, spool)
		r := cs(t, spool, "", args...)
		if r.code != exitOK {
			t.Fatalf("%v: %+v", args, r)
		}
		// The decision as printed, and the first entry of the log.
		for name, out := range map[string]string{"decide": r.stdout, "log": cs(t, spool, "", "log").stdout} {
			var got struct {
				Record struct{ Waybill json.RawMessage }
			}
			if err := json.NewDecoder(strings.NewReader(out)).Decode(&got); err != nil {
				t.Fatal(err)
			}
			var have bytes.Buffer
			if err := json.Compact(&have, got.Record.Waybill); err != nil || !bytes.Equal(have.Bytes(), want.Bytes()) {
				t.Errorf("%s %v: waybill\n%s\nwant\n%s", name, args, got.Record.Waybill, want.Bytes())
			}
		}
	}
}

// capture is a Muster that keeps the result of the one complete call it is
// given, and otherwise answers as the stand-in does.
type capture struct {
	muster.Queue
	lease  string
	state  item.State
	result []byte
}

func (c *capture) Complete(ctx context.Context, lease string, state item.State, result []byte) error {
	c.lease, c.state, c.result = lease, state, append([]byte(nil), result...)
	return c.Queue.Complete(ctx, lease, state, result)
}

// forbidden are the names Waybill refuses anywhere in results.countersign
// (forbiddenObservationKeys in waybill/internal/buildspec).
var forbidden = []string{
	"html", "raw_html", "raw_text", "body_text", "page_text", "dom", "headers", "response_headers",
	"screenshot", "screenshots", "snapshot", "snapshots", "observation", "observations",
}

func anyKey(v any, names []string) bool {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			for _, n := range names {
				if k == n {
					return true
				}
			}
			if anyKey(child, names) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if anyKey(child, names) {
				return true
			}
		}
	}
	return false
}

// The golden test: an approval with a specification goes through the worker
// to Muster, and what Muster is given as the result is what Waybill's
// extractor takes as results.countersign (waybill/internal/worker,
// extractCountersignRecord, commit 52c51a9): an object under "waybill", an
// outcome of "approve" if any, no "record" next to it, no forbidden key
// anywhere, and the record Waybill's own example holds, unchanged.
func TestGoldenApprovalWithWaybillIsWhatWaybillAccepts(t *testing.T) {
	spool := t.TempDir()
	st, err := store.Open(spool)
	if err != nil {
		t.Fatal(err)
	}
	seed := muster.Seed{Ownership: []string{"catalog/sample/description"}}
	seed.Steps = append(seed.Steps, json.RawMessage(`{"kind":"item","id":"PA-0001","state":"scored","lane":"normal","results":{"assay":{"score":82,"recommendation":"build"}}}`))
	standin, err := muster.NewStandIn(seed, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	q := &capture{Queue: standin}
	w := &worker.Worker{Store: st, Queue: q, Now: func() time.Time { return at }, Log: func(worker.Event) {}}

	w.Tick(context.Background()) // the item arrives under a lease
	r := cs(t, spool, "", "decide", "PA-0001", "approve", "-waybill", writeFile(t, readSpec(t)))
	if r.code != exitOK {
		t.Fatalf("decide: %+v", r)
	}
	w.Tick(context.Background()) // the decision goes to Muster
	if q.result == nil || q.state != item.Approved {
		t.Fatalf("Muster was given no approval: state %q", q.state)
	}

	body, err := json.Marshal(map[string]any{"contract": "v1", "lease": q.lease, "state": q.state, "result": json.RawMessage(q.result)})
	if err != nil || len(body) > 64<<10 {
		t.Fatalf("the complete body is %d bytes: %v", len(body), err)
	}
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(q.result, &outer); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := json.Unmarshal(outer["outcome"], &outcome); err != nil || outcome != "approve" {
		t.Errorf("outcome %q: %v", outcome, err)
	}
	if _, ok := outer["record"]; ok {
		t.Error("both waybill and record are present")
	}
	var tree any
	if err := json.Unmarshal(q.result, &tree); err != nil || anyKey(tree, forbidden) {
		t.Errorf("the result holds a key Waybill refuses: %v", err)
	}
	var want bytes.Buffer
	if err := json.Compact(&want, []byte(readSpec(t))); err != nil {
		t.Fatal(err)
	}
	var have bytes.Buffer
	if err := json.Compact(&have, outer["waybill"]); err != nil || !bytes.Equal(have.Bytes(), want.Bytes()) {
		t.Errorf("waybill\n%s\nwant\n%s", have.Bytes(), want.Bytes())
	}
	if snap := standin.Snapshot(); len(snap.Ledger) != 1 || snap.Ledger[0].Result == nil || len(snap.Ledger[0].Result.Waybill) == 0 {
		t.Errorf("Countersign's own Muster stand-in did not keep the waybill: %+v", snap)
	}
}

func TestOverrideWithAWaybillIsRefused(t *testing.T) {
	spool := t.TempDir()
	addAll(t, spool)
	r := cs(t, spool, "", "decide", "PA-0002", "override", "-reason", "Reshaped to do one job.", "-waybill", writeFile(t, readSpec(t)))
	if r.code != exitError || r.stdout != "" || !strings.HasPrefix(r.stderr, "E_PARSE decision.field_unexpected PA-0002:") {
		t.Fatalf("override with a waybill: %+v", r)
	}
	if l := cs(t, spool, "", "log"); l.stdout != "" {
		t.Errorf("a refused decision was spooled: %s", l.stdout)
	}
	// Without the flag the override still works.
	if r = cs(t, spool, "", "decide", "PA-0002", "override", "-reason", "Reshaped to do one job."); r.code != exitOK {
		t.Errorf("override: %+v", r)
	}
}

func TestDecideAcceptsASpecificationAtTheSizeLimit(t *testing.T) {
	spool := t.TempDir()
	addAll(t, spool)
	r := cs(t, spool, "", "decide", "PA-0001", "approve", "-waybill", writeFile(t, paddedSpec(t, item.MaxWaybill)))
	if r.code != exitOK {
		t.Fatalf("a specification of exactly the limit: %+v", r)
	}
}

func TestReviewRefusesToApproveACandidateWithoutASpecification(t *testing.T) {
	spool := t.TempDir()
	addAll(t, spool)
	want := "countersign: review cannot approve a candidate: use decide ID approve -waybill FILE\n"
	r := cs(t, spool, "approve\nquit\n", "review", "PA-0001")
	if r.code != exitOK || r.stderr != want || strings.Contains(r.stdout, "recorded") {
		t.Fatalf("review approve: %+v", r)
	}
	if l := cs(t, spool, "", "log"); l.stdout != "" {
		t.Errorf("an approval without a specification was spooled: %s", l.stdout)
	}
	// The other outcomes, and approving an edit proposal, are unchanged.
	if r = cs(t, spool, "reject\nLow score.\n", "review", "PA-0001"); r.code != exitOK || !strings.Contains(r.stdout, "recorded PA-0001") {
		t.Errorf("review reject: %+v", r)
	}
	if r = cs(t, spool, "approve\n\n", "review", "EP-0001"); r.code != exitOK || !strings.Contains(r.stdout, "recorded EP-0001") {
		t.Errorf("review approve of an edit proposal: %+v", r)
	}
}
