// Command countersign holds scored candidates and edit proposals until a
// person decides.
//
//	countersign [-spool DIR] add [FILE]
//	countersign [-spool DIR] list
//	countersign [-spool DIR] show ID
//	countersign [-spool DIR] decide ID OUTCOME [-reason TEXT] [-tool TEXT] [-parent TEXT] [-blocker TEXT]
//	countersign [-spool DIR] decide -f FILE
//	countersign [-spool DIR] log
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
  list                  list the items waiting for a decision
  show ID               show one waiting item and the outcomes allowed for it
  decide ID OUTCOME     record a decision; flags: -reason -tool -parent -blocker
  decide -f FILE        record a decision given as JSON ("-" is standard input)
  log                   list recent records and whether Muster has confirmed them
  run -standin FILE     run the worker loop against the in-memory Muster stand-in
  rules                 list every rule and its public error code
  version               print the version

outcomes: approve link merge park reject (a scored candidate)
          override (a screened-out candidate)
          approve decline (an edit proposal)
`

type env struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	now    func() time.Time
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
	}
	os.Exit(run(os.Args[1:], e))
}

func run(args []string, e env) int {
	global := flag.NewFlagSet("countersign", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	spool := global.String("spool", DefaultSpool, "")
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
	case "add", "list", "show", "decide", "log", "run":
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
	case "list":
		return cmdList(e, st)
	case "show":
		return cmdShow(e, st, rest)
	case "decide":
		return cmdDecide(e, st, rest)
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
	in, err := st.Intake("", payload, e.now())
	if err != nil {
		return public(e, err)
	}
	if in.Err != nil {
		return public(e, in.Err)
	}
	printJSON(e.stdout, in.Item, true)
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
			detail, summary = it.Type+" "+it.Target, it.Body
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
		if len(words) != 0 || *reason != "" || *tool != "" || *parent != "" || *blocker != "" {
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
	}
	res, err := st.Decide(d, e.now())
	if err != nil {
		return public(e, err)
	}
	printJSON(e.stdout, res, true)
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
	standin := fs.String("standin", "", "")
	interval := fs.Duration("interval", 15*time.Second, "")
	ticks := fs.Int("ticks", 0, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *interval <= 0 || *ticks < 0 {
		return usageError(e, "run: see countersign help")
	}
	if *standin == "" {
		return usageError(e, "run needs -standin FILE: the client for the real Muster is not built yet")
	}
	data, err := os.ReadFile(*standin)
	if err != nil {
		return usageError(e, "cannot read the stand-in file")
	}
	seed, err := muster.ParseSeed(data)
	if err != nil {
		return usageError(e, "the stand-in file is not a usable seed")
	}
	queue, err := muster.NewStandIn(seed, e.now)
	if err != nil {
		return usageError(e, "the stand-in file is not a usable seed")
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
	if *ticks > 0 {
		// A bounded run is for trying things out: show where it ended.
		printJSON(e.stdout, queue.Snapshot(), true)
	}
	return exitOK
}
