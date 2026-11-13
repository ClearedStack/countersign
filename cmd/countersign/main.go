// Command countersign holds scored candidates and edit proposals until a
// person decides, and records what they decided.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/clearedstack/countersign/internal/fault"
	"github.com/clearedstack/countersign/internal/muster"
	"github.com/clearedstack/countersign/internal/queue"
	"github.com/clearedstack/countersign/internal/store"
)

// Exit codes.
const (
	exitOK      = 0
	exitRefused = 1 // refused with one of the three public error codes
	exitUsage   = 2 // the command line was wrong
	exitIO      = 3 // a file could not be read or written
)

const usage = `usage: countersign -store FILE COMMAND

commands:
  lease -from FILE                        hold the items in a saved Muster lease
  pending                                 list the items waiting for a decision
  decide -id ID -outcome WORD [-reason TEXT]
                                          record a decision
  decisions                               list the recorded decisions
  outcomes                                list the allowed outcomes (needs no -store)
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("countersign", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	storePath := global.String("store", "", "")
	if err := global.Parse(args); err != nil || global.NArg() == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cmd, rest := global.Arg(0), global.Args()[1:]

	sub := flag.NewFlagSet(cmd, flag.ContinueOnError)
	sub.SetOutput(io.Discard)
	from := sub.String("from", "", "")
	id := sub.String("id", "", "")
	outcome := sub.String("outcome", "", "")
	reason := sub.String("reason", "", "")
	if err := sub.Parse(rest); err != nil || sub.NArg() != 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	var err error
	switch {
	case cmd == "outcomes":
		printOutcomes(stdout)
	case *storePath == "":
		fmt.Fprint(stderr, usage)
		return exitUsage
	case cmd == "lease" && *from != "":
		err = lease(*storePath, *from, stdout)
	case cmd == "pending":
		err = pending(*storePath, stdout)
	case cmd == "decide" && *id != "" && *outcome != "":
		err = decide(*storePath, *id, *outcome, *reason, stdout)
	case cmd == "decisions":
		err = decisions(*storePath, stdout)
	default:
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	var ferr *fault.Error
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &ferr):
		fmt.Fprintln(stderr, ferr.Error())
		return exitRefused
	default:
		// Only the fixed text: the underlying cause would name a path.
		fmt.Fprintln(stderr, "countersign: a file could not be read or written")
		return exitIO
	}
}

func printOutcomes(w io.Writer) {
	for _, r := range queue.Rules() {
		need := "reason-optional"
		if r.NeedsReason {
			need = "reason-required"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Kind, r.From, r.Outcome, r.To, need)
	}
}

// lease takes every item from the Muster stand-in and holds the ones that
// can be held. One bad item never stops the rest.
func lease(storePath, from string, w io.Writer) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return store.ErrIO
	}
	var client muster.Client
	client, err = muster.FromJSON(data)
	if err != nil {
		return err
	}
	s, err := store.Load(storePath)
	if err != nil {
		return err
	}
	raws, err := client.Lease(context.Background())
	if err != nil {
		return err
	}
	for n, raw := range raws {
		it, ferr := queue.Admit(raw)
		switch {
		case ferr != nil:
			fmt.Fprintf(w, "refused\t#%d\t%s\t%s\n", n+1, ferr.Code, ferr.Fingerprint())
		case s.Has(it.ID):
			fmt.Fprintf(w, "known\t%s\n", it.ID)
		case it.Paused != "":
			s.Hold(it)
			fmt.Fprintf(w, "paused\t%s\t%s\t%s\n", it.ID, it.Paused, fault.NeedsSubtool)
		default:
			s.Hold(it)
			fmt.Fprintf(w, "held\t%s\t%s\t%s\n", it.ID, it.Kind, it.State)
		}
	}
	return s.Save(storePath)
}

func pending(storePath string, w io.Writer) error {
	s, err := store.Load(storePath)
	if err != nil {
		return err
	}
	for _, it := range s.Pending() {
		allowed := strings.Join(queue.Allowed(it), ",")
		if it.Paused != "" {
			allowed = it.Paused + ":" + string(fault.NeedsSubtool)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", it.ID, plain(it.Kind), plain(it.State), allowed, strconv.QuoteToASCII(it.Title))
	}
	return nil
}

// plain returns a word as it is when it is safe to print in a column, and
// quoted otherwise. Kinds and states of paused items come from outside.
func plain(word string) string {
	for i := 0; i < len(word); i++ {
		if word[i] <= ' ' || word[i] > '~' || word[i] == '"' {
			return strconv.QuoteToASCII(word)
		}
	}
	if word == "" {
		return `""`
	}
	return word
}

func decide(storePath, id, outcome, reason string, w io.Writer) error {
	s, err := store.Load(storePath)
	if err != nil {
		return err
	}
	d, ferr := s.Decide(id, outcome, reason)
	if ferr != nil {
		return ferr
	}
	if err := s.Save(storePath); err != nil {
		return err
	}
	fmt.Fprintf(w, "recorded\t%d\t%s\t%s\t%s\t%s\n", d.Seq, d.ItemID, d.Outcome, d.From, d.To)
	return nil
}

func decisions(storePath string, w io.Writer) error {
	s, err := store.Load(storePath)
	if err != nil {
		return err
	}
	for _, d := range s.Decisions {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", d.Seq, d.ItemID, d.Outcome, d.From, d.To, strconv.QuoteToASCII(d.Reason))
	}
	return nil
}
