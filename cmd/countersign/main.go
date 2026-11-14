// Command countersign holds scored candidates and edit proposals until a
// person decides.
//
//	countersign [-spool DIR] add [FILE]
//	countersign [-spool DIR] status [-json]
//	countersign [-spool DIR] list
//	countersign [-spool DIR] show ID
//	countersign [-spool DIR] review [-kind item|proposal] [-lane urgent|normal|backfill] [ID...]
//	countersign [-spool DIR] decide ID OUTCOME [-reason TEXT] [-tool TEXT] [-parent TEXT] [-blocker TEXT] [-waybill FILE]
//	countersign [-spool DIR] decide -f FILE
//	countersign [-spool DIR] release ID [-reason TEXT]
//	countersign [-spool DIR] held
//	countersign [-spool DIR] log
//	countersign [-spool DIR] run -muster URL [-token-file FILE] [-interval D]
//	countersign [-spool DIR] run -standin FILE [-interval D] [-ticks N]
//	countersign rules
//	countersign version
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ClearedStack/countersign/internal/decide"
	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/muster"
	"github.com/ClearedStack/countersign/internal/record"
	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/store"
	"github.com/ClearedStack/countersign/internal/worker"
)

// Version is the component's version in the naming registry.
const Version = "v0.1.0-draft"

// DefaultSpool is the only path the pod may write to.
const DefaultSpool = "/var/spool/cs"

// memoryLimit keeps the Go runtime well inside the 30 MiB budget.
const memoryLimit = 20 << 20

const (
	exitOK    = 0
	exitError = 1 // a public error was printed
	exitUsage = 2
)

const usage = `countersign holds scored candidates and edit proposals until a person decides.

usage: countersign [-spool DIR] COMMAND

  add [FILE]            check in one item (JSON) from FILE or standard input
  status [-json]        summarize inbox, outbox, stopped and settled counts
  list                  list the items waiting for a decision
  show ID               show one waiting item and the outcomes allowed for it
  review [ID...]        step through waiting items interactively; flags: -kind -lane
  decide ID OUTCOME     record a decision; flags: -reason -tool -parent -blocker
                        -waybill FILE (approve of a scored item, or override of
                        a screened_out one: the specification the owner reviewed)
  decide -f FILE        record a decision given as JSON ("-" is standard input)
  release ID            let an item run again that a worker stopped with E_PARSE;
                        flag: -reason (kept in the local log only)
  held                  list the items Countersign itself stopped with E_PARSE
  log                   list recent records and whether Muster has confirmed them
  run -muster URL       run the lease loop against Muster (or set MUSTER_URL);
                        -token-file names the ServiceAccount token, read afresh
                        for every call; CONTRACT_VERSION, if set, must be v1
  run -standin FILE     run the lease loop against the in-memory Muster stand-in
  rules                 list every rule and its public error code
  version               print the version

outcomes: approve link merge park reject (a scored item)
          override (a screened_out item)
          approve decline (an edit proposal)
`

type env struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	now    func() time.Time
	getenv func(string) string
}

func (e env) envVar(name string) string {
	if e.getenv == nil {
		return ""
	}
	return e.getenv(name)
}

func main() {
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(memoryLimit)
	}
	e := env{
		stdin:  os.Stdin,
		stdout: os.Stdout,
		stderr: os.Stderr,
		now:    func() time.Time { return time.Now().UTC().Truncate(time.Second) },
		getenv: os.Getenv,
	}
	os.Exit(run(os.Args[1:], e))
}

func run(args []string, e env) int {
	global := flag.NewFlagSet("countersign", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	defaultSpool := DefaultSpool
	if v := e.envVar("COUNTERSIGN_SPOOL"); v != "" {
		defaultSpool = v
	}
	spool := global.String("spool", defaultSpool, "")
	if err := global.Parse(args); err != nil {
		fmt.Fprint(e.stderr, usage)
		return exitUsage
	}
	rest := global.Args()
	if len(rest) == 0 {
		fmt.Fprint(e.stderr, usage)
		return exitUsage
	}
	cmd, rest := rest[0], rest[1:]

	switch cmd {
	case "version":
		fmt.Fprintln(e.stdout, "countersign", Version)
		return exitOK
	case "rules":
		return cmdRules(e)
	case "help":
		fmt.Fprint(e.stdout, usage)
		return exitOK
	case "add", "status", "list", "show", "review", "decide", "release", "held", "log", "run":
	default:
		fmt.Fprint(e.stderr, usage)
		return exitUsage
	}

	st, err := store.Open(*spool)
	if err != nil {
		return public(e, err)
	}
	switch cmd {
	case "add":
		return cmdAdd(e, st, rest)
	case "status":
		return cmdStatus(e, st, rest)
	case "list":
		return cmdList(e, st)
	case "show":
		return cmdShow(e, st, rest)
	case "review":
		return cmdReview(e, st, rest)
	case "decide":
		return cmdDecide(e, st, rest)
	case "release":
		return cmdRelease(e, st, rest)
	case "held":
		return cmdHeld(e, st)
	case "log":
		return cmdLog(e, st)
	default:
		return cmdRun(e, st, rest)
	}
}

// public prints an error as a public error line. Anything that is not
// already public is reported as the store being unavailable, without its
// text.
func public(e env, err error) int {
	fmt.Fprintln(e.stderr, rule.Public(err, rule.StoreUnavailable, "").Error())
	return exitError
}

func usageError(e env, msg string) int {
	fmt.Fprintln(e.stderr, "countersign: "+msg)
	return exitUsage
}

func printJSON(w io.Writer, v any, indent bool) {
	enc := json.NewEncoder(w)
	if indent {
		enc.SetIndent("", "  ")
	}
	enc.Encode(v)
}

// readInput reads a bounded amount from a file or standard input. A file
// that cannot be read is a usage error: its name is the caller's own.
func readInput(e env, name string) ([]byte, bool) {
	var r io.Reader = e.stdin
	if name != "" && name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return nil, false
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, item.MaxPayload+1))
	if err != nil {
		return nil, false
	}
	return b, true
}

// readWaybill reads the specification file the owner attached to an approval.
// It takes a regular file of at most item.MaxWaybill bytes and nothing else:
// not standard input, a directory or a pipe. Why it failed is not said.
func readWaybill(name string) ([]byte, bool) {
	f, err := os.Open(name)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(f, item.MaxWaybill+1))
	if err != nil || len(b) == 0 || len(b) > item.MaxWaybill {
		return nil, false
	}
	return b, true
}

func cmdRules(e env) int {
	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RULE\tCODE\tMEANING")
	for _, info := range rule.All() {
		code := string(info.Code)
		if code == "" {
			code = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", info.ID, code, info.Text)
	}
	tw.Flush()
	return exitOK
}

func cmdAdd(e env, st *store.Store, args []string) int {
	if len(args) > 1 {
		return usageError(e, "add takes at most one file")
	}
	name := ""
	if len(args) == 1 {
		name = args[0]
	}
	payload, ok := readInput(e, name)
	if !ok {
		return usageError(e, "cannot read the input")
	}
	it, perr := item.Parse(payload)
	if perr != nil {
		return public(e, perr)
	}
	// An item added by hand has no lease, so a decision on it is recorded
	// here and cannot be sent to Muster.
	if _, err := st.Accept("", it); err != nil {
		return public(e, err)
	}
	printJSON(e.stdout, it, true)
	return exitOK
}

// oneLine makes held text fit one row of a listing. Text was checked on the
// way in, so it holds no control characters other than new lines and tabs.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 { // do not split a character
		cut--
	}
	return s[:cut] + "..."
}

func cmdList(e env, st *store.Store) int {
	held, err := st.Pending()
	if err != nil {
		return public(e, err)
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKIND\tSTATE\tLANE\tDETAIL\tSUMMARY")
	for _, h := range held {
		it := h.Item
		detail, summary := "", ""
		switch {
		case it.Kind == item.Proposal:
			detail, summary = it.EditKind+" "+it.Target, it.Change
		case it.State == item.Scored:
			detail, summary = fmt.Sprintf("score %d, recommended %s", *it.Score, it.Recommendation), it.Title
		default:
			detail, summary = oneLine(it.ScreenReason, 48), it.Title
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", it.ID, it.Kind, it.State, it.Lane, detail, oneLine(summary, 60))
	}
	tw.Flush()
	return exitOK
}

func cmdShow(e env, st *store.Store, args []string) int {
	if len(args) != 1 {
		return usageError(e, "show takes one item identifier")
	}
	h, err := st.Get(args[0])
	if err != nil {
		return public(e, err)
	}
	printJSON(e.stdout, struct {
		Item     item.Item      `json:"item"`
		Outcomes []item.Outcome `json:"outcomes"`
	}{h.Item, item.Outcomes(h.Item.Kind, h.Item.State)}, true)
	return exitOK
}

func cmdDecide(e env, st *store.Store, args []string) int {
	fs := flag.NewFlagSet("decide", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	file := fs.String("f", "", "")
	reason := fs.String("reason", "", "")
	tool := fs.String("tool", "", "")
	parent := fs.String("parent", "", "")
	blocker := fs.String("blocker", "", "")
	waybill := fs.String("waybill", "", "")

	// Flags may come before, between or after the identifier and outcome.
	var words []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return usageError(e, "decide: unknown flag; see countersign help")
		}
		args = fs.Args()
		if len(args) > 0 {
			words = append(words, args[0])
			args = args[1:]
		}
	}

	var d decide.Decision
	if *file != "" {
		if len(words) != 0 || *reason != "" || *tool != "" || *parent != "" || *blocker != "" || *waybill != "" {
			return usageError(e, "decide -f takes the whole decision from the file")
		}
		payload, ok := readInput(e, *file)
		if !ok {
			return usageError(e, "cannot read the input")
		}
		var perr *rule.Error
		if d, perr = decide.Parse(payload); perr != nil {
			return public(e, perr)
		}
	} else {
		if len(words) != 2 {
			return usageError(e, "decide takes an item identifier and an outcome")
		}
		d = decide.Decision{
			Item:    words[0],
			Outcome: item.Outcome(words[1]),
			Reason:  *reason,
			Tool:    *tool,
			Parent:  *parent,
			Blocker: *blocker,
		}
		if *waybill != "" {
			spec, ok := readWaybill(*waybill)
			if !ok {
				return public(e, rule.Fail(rule.DecisionMalformed, d.Item))
			}
			d.Waybill = spec
		}
	}
	res, err := st.Decide(d, e.now())
	if err != nil {
		return public(e, err)
	}
	printJSON(e.stdout, res, true)
	return exitOK
}

func cmdRelease(e env, st *store.Store, args []string) int {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	reason := fs.String("reason", "", "")
	var words []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return usageError(e, "release: unknown flag; see countersign help")
		}
		args = fs.Args()
		if len(args) > 0 {
			words = append(words, args[0])
			args = args[1:]
		}
	}
	if len(words) != 1 {
		return usageError(e, "release takes one item identifier")
	}
	res, err := st.Release(words[0], *reason, e.now())
	if err != nil {
		return public(e, err)
	}
	printJSON(e.stdout, res, true)
	return exitOK
}

type heldItem struct {
	id   string
	at   string
	rule string
}

func stoppedItems(entries []store.Entry) []heldItem {
	stopped := map[string]heldItem{}
	var order []string
	for _, en := range entries {
		rec := en.Record
		switch {
		case rec.Call == record.Fail && rec.Code == string(rule.Parse) && en.Status == store.Acknowledged && rec.Item != "":
			if _, seen := stopped[rec.Item]; !seen {
				order = append(order, rec.Item)
			}
			stopped[rec.Item] = heldItem{id: rec.Item, at: rec.At, rule: rec.Rule}
		case rec.Call == record.Release && en.Status != store.Refused:
			delete(stopped, rec.Item)
		}
	}
	var out []heldItem
	for _, id := range order {
		if h, ok := stopped[id]; ok {
			out = append(out, h)
		}
	}
	return out
}

// cmdHeld lists the items this Countersign stopped with E_PARSE and has not
// since released, as far back as the local log goes. Items other workers
// stopped are not known here.
func cmdHeld(e env, st *store.Store) int {
	entries, err := st.Log()
	if err != nil {
		return public(e, err)
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTOPPED\tRULE")
	for _, h := range stoppedItems(entries) {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", h.id, h.at, h.rule)
	}
	tw.Flush()
	return exitOK
}

type statusSummary struct {
	Inbox struct {
		Total       int `json:"total"`
		Urgent      int `json:"urgent"`
		Normal      int `json:"normal"`
		Backfill    int `json:"backfill"`
		Scored      int `json:"scored"`
		ScreenedOut int `json:"screened_out"`
		Proposals   int `json:"proposals"`
	} `json:"inbox"`
	Outbox struct {
		Total    int `json:"total"`
		Waiting  int `json:"waiting"`
		Unleased int `json:"unleased"`
	} `json:"outbox"`
	Held int `json:"held"`
	Log  struct {
		Settled      int `json:"settled"`
		Acknowledged int `json:"acknowledged"`
		Refused      int `json:"refused"`
	} `json:"log"`
}

func cmdStatus(e env, st *store.Store, args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError(e, "status: see countersign help")
	}

	pending, err := st.Pending()
	if err != nil {
		return public(e, err)
	}
	entries, err := st.Log()
	if err != nil {
		return public(e, err)
	}

	var s statusSummary
	s.Inbox.Total = len(pending)
	for _, h := range pending {
		switch h.Item.Lane {
		case item.Urgent:
			s.Inbox.Urgent++
		case item.Normal:
			s.Inbox.Normal++
		case item.Backfill:
			s.Inbox.Backfill++
		}
		switch {
		case h.Item.Kind == item.Proposal:
			s.Inbox.Proposals++
		case h.Item.State == item.Scored:
			s.Inbox.Scored++
		case h.Item.State == item.ScreenedOut:
			s.Inbox.ScreenedOut++
		}
	}
	for _, en := range entries {
		switch en.Status {
		case store.Waiting:
			s.Outbox.Total++
			s.Outbox.Waiting++
		case store.Unleased:
			s.Outbox.Total++
			s.Outbox.Unleased++
		case store.Acknowledged:
			s.Log.Settled++
			s.Log.Acknowledged++
		case store.Refused:
			s.Log.Settled++
			s.Log.Refused++
		}
	}
	s.Held = len(stoppedItems(entries))

	if *asJSON {
		printJSON(e.stdout, s, true)
		return exitOK
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "INBOX\t%d waiting\t(urgent %d, normal %d, backfill %d; scored %d, screened_out %d, proposals %d)\n",
		s.Inbox.Total, s.Inbox.Urgent, s.Inbox.Normal, s.Inbox.Backfill, s.Inbox.Scored, s.Inbox.ScreenedOut, s.Inbox.Proposals)
	fmt.Fprintf(tw, "OUTBOX\t%d queued\t(waiting %d, unleased %d)\n",
		s.Outbox.Total, s.Outbox.Waiting, s.Outbox.Unleased)
	fmt.Fprintf(tw, "HELD\t%d stopped\t(local E_PARSE awaiting release)\n", s.Held)
	fmt.Fprintf(tw, "LOG\t%d settled\t(acknowledged %d, refused %d)\n",
		s.Log.Settled, s.Log.Acknowledged, s.Log.Refused)
	tw.Flush()
	return exitOK
}

func cmdLog(e env, st *store.Store) int {
	entries, err := st.Log()
	if err != nil {
		return public(e, err)
	}
	for _, en := range entries {
		printJSON(e.stdout, en, false)
	}
	return exitOK
}

func cmdRun(e env, st *store.Store, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	address := fs.String("muster", "", "")
	tokenFile := fs.String("token-file", muster.DefaultTokenFile, "")
	standin := fs.String("standin", "", "")
	interval := fs.Duration("interval", worker.Interval, "")
	ticks := fs.Int("ticks", 0, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *interval <= 0 || *ticks < 0 {
		return usageError(e, "run: see countersign help")
	}
	if *address == "" && *standin == "" {
		*address = e.envVar("MUSTER_URL")
	}
	if (*address == "") == (*standin == "") {
		return usageError(e, "run needs exactly one of -muster URL and -standin FILE")
	}

	var queue muster.Queue
	var snapshot func() muster.Snapshot
	if *address != "" {
		if v := e.envVar("CONTRACT_VERSION"); v != "" && v != record.ContractVersion {
			return usageError(e, "CONTRACT_VERSION must be v1")
		}
		client, err := muster.NewHTTP(*address, *tokenFile)
		if err != nil {
			return usageError(e, "the address given to -muster must be a plain http or https URL")
		}
		queue = client
	} else {
		data, err := os.ReadFile(*standin)
		if err != nil {
			return usageError(e, "cannot read the stand-in file")
		}
		seed, err := muster.ParseSeed(data)
		if err != nil {
			return usageError(e, "the stand-in file is not a usable seed")
		}
		s, err := muster.NewStandIn(seed, e.now)
		if err != nil {
			return usageError(e, "the stand-in file is not a usable seed")
		}
		queue, snapshot = s, s.Snapshot
	}

	w := &worker.Worker{
		Store: st,
		Queue: queue,
		Now:   e.now,
		Log:   func(ev worker.Event) { printJSON(e.stderr, ev, false) },
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	timer := time.NewTicker(*interval)
	defer timer.Stop()
	for n := 1; ; n++ {
		w.Tick(ctx)
		if *ticks > 0 && n >= *ticks {
			break
		}
		select {
		case <-ctx.Done():
			return exitOK
		case <-timer.C:
		}
	}
	if snapshot != nil {
		// A bounded run against the stand-in is for trying things out: show
		// where it ended.
		printJSON(e.stdout, snapshot(), true)
	}
	return exitOK
}
