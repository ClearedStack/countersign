package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ClearedStack/countersign/internal/decide"
	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/store"
)

func cmdReview(e env, st *store.Store, args []string) int {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	kindFlag := fs.String("kind", "", "")
	laneFlag := fs.String("lane", "", "")

	var ids []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return usageError(e, "review: unknown flag; see countersign help")
		}
		args = fs.Args()
		if len(args) > 0 {
			ids = append(ids, args[0])
			args = args[1:]
		}
	}

	switch *kindFlag {
	case "", string(item.Candidate), string(item.Proposal):
	default:
		return usageError(e, "review: -kind must be item or proposal")
	}
	switch *laneFlag {
	case "", string(item.Urgent), string(item.Normal), string(item.Backfill):
	default:
		return usageError(e, "review: -lane must be urgent, normal or backfill")
	}
	if len(ids) > 0 && (*kindFlag != "" || *laneFlag != "") {
		return usageError(e, "review takes either item identifiers or -kind/-lane filters")
	}

	var queue []string
	if len(ids) > 0 {
		seen := make(map[string]bool, len(ids))
		for _, id := range ids {
			if _, err := st.Get(id); err != nil {
				return public(e, err)
			}
			if !seen[id] {
				seen[id] = true
				queue = append(queue, id)
			}
		}
	} else {
		held, err := st.Pending()
		if err != nil {
			return public(e, err)
		}
		for _, h := range held {
			if *kindFlag != "" && string(h.Item.Kind) != *kindFlag {
				continue
			}
			if *laneFlag != "" && string(h.Item.Lane) != *laneFlag {
				continue
			}
			queue = append(queue, h.Item.ID)
		}
	}

	if len(queue) == 0 {
		fmt.Fprintln(e.stdout, "no items waiting.")
		return exitOK
	}

	reader := bufio.NewReaderSize(e.stdin, 4096)
	overLimit := strings.Repeat("x", item.MaxText+1)

	for i, id := range queue {
		h, err := st.Get(id)
		if err != nil {
			var perr *rule.Error
			if errors.As(err, &perr) && perr.Rule == rule.DecisionItemUnknown {
				fmt.Fprintf(e.stdout, "skipped %s: no longer waiting.\n", id)
				continue
			}
			return public(e, err)
		}

		it := h.Item
		printReviewCard(e.stdout, i+1, len(queue), it)

		for {
			fmt.Fprint(e.stdout, "decision [skip/quit]: ")
			choiceLine, tooLong, eof, rerr := readPromptLine(reader)
			if rerr != nil {
				return usageError(e, "cannot read standard input")
			}
			if eof {
				fmt.Fprintln(e.stdout)
				return exitOK
			}
			choice := strings.TrimSpace(choiceLine)
			if tooLong {
				fmt.Fprintln(e.stderr, rule.Fail(rule.DecisionOutcomeUnknown, it.ID).Error())
				continue
			}
			switch choice {
			case "", "skip", "s":
				fmt.Fprintf(e.stdout, "skipped %s.\n", it.ID)
				goto nextItem
			case "quit", "q":
				return exitOK
			}

			outcome := item.Outcome(choice)
			if !item.KnownOutcome(outcome) {
				fmt.Fprintln(e.stderr, rule.Fail(rule.DecisionOutcomeUnknown, it.ID).Error())
				continue
			}
			to, ok := item.Next(it.Kind, it.State, outcome)
			if !ok {
				fmt.Fprintln(e.stderr, rule.Fail(rule.DecisionOutcomeNotAllowed, it.ID).Error())
				continue
			}

			d := decide.Decision{Item: it.ID, Outcome: outcome}
			switch outcome {
			case item.Link:
				val, long, stop, err := promptField(reader, e.stdout, "existing tool (required): ")
				if err != nil {
					return usageError(e, "cannot read standard input")
				}
				if stop {
					return exitOK
				}
				if long {
					val = overLimit
				}
				d.Tool = val
			case item.Merge:
				val, long, stop, err := promptField(reader, e.stdout, "parent tool (required): ")
				if err != nil {
					return usageError(e, "cannot read standard input")
				}
				if stop {
					return exitOK
				}
				if long {
					val = overLimit
				}
				d.Parent = val
			case item.Park:
				val, long, stop, err := promptField(reader, e.stdout, "blocker (required): ")
				if err != nil {
					return usageError(e, "cannot read standard input")
				}
				if stop {
					return exitOK
				}
				if long {
					val = overLimit
				}
				d.Blocker = val
			}

			needReason := outcome == item.Reject ||
				outcome == item.Merge ||
				it.State == item.ScreenedOut ||
				(it.Kind == item.Candidate && it.State == item.Scored && to != it.Recommendation)
			reasonLabel := "reason (optional): "
			if needReason {
				reasonLabel = "reason (required): "
			}
			reasonVal, long, stop, err := promptField(reader, e.stdout, reasonLabel)
			if err != nil {
				return usageError(e, "cannot read standard input")
			}
			if stop {
				return exitOK
			}
			if long {
				reasonVal = overLimit
			}
			d.Reason = reasonVal

			res, derr := st.Decide(d, e.now())
			if derr != nil {
				var perr *rule.Error
				if !errors.As(derr, &perr) || perr.Code() != rule.Parse {
					return public(e, derr)
				}
				fmt.Fprintln(e.stderr, perr.Error())
				if perr.Rule == rule.DecisionItemUnknown {
					break
				}
				continue
			}

			tag := ""
			if res.Record.Override {
				tag = " [override]"
			}
			fmt.Fprintf(e.stdout, "recorded %s: %s -> %s (%s)%s\n", it.ID, res.Record.From, res.Record.To, res.Rule, tag)
			break
		}
	nextItem:
	}
	return exitOK
}

func promptField(r *bufio.Reader, out io.Writer, prompt string) (val string, tooLong bool, eof bool, err error) {
	fmt.Fprint(out, prompt)
	line, tooLong, eof, err := readPromptLine(r)
	if err != nil {
		return "", false, false, err
	}
	if eof {
		fmt.Fprintln(out)
		return "", false, true, nil
	}
	return line, tooLong, false, nil
}

// readPromptLine reads one line up to item.MaxPayload bytes. If the line is
// longer, the rest of the line is discarded and tooLong is true so memory use
// stays bounded.
func readPromptLine(r *bufio.Reader) (line string, tooLong bool, eof bool, err error) {
	var buf []byte
	for {
		frag, rerr := r.ReadSlice('\n')
		if len(buf)+len(frag) <= item.MaxPayload+1 {
			buf = append(buf, frag...)
		} else {
			tooLong = true
		}
		if rerr == nil {
			break
		}
		if errors.Is(rerr, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(rerr, io.EOF) {
			if len(buf) == 0 && !tooLong {
				return "", false, true, nil
			}
			break
		}
		return "", false, false, rerr
	}
	if tooLong {
		return "", true, false, nil
	}
	s := strings.TrimSuffix(string(buf), "\n")
	s = strings.TrimSuffix(s, "\r")
	return s, false, false, nil
}

func recommendedOutcome(rec item.State) string {
	switch rec {
	case item.Approved:
		return string(item.Approve)
	case item.Linked:
		return string(item.Link)
	case item.Parked:
		return string(item.Park)
	case item.Rejected:
		return string(item.Reject)
	default:
		return string(rec)
	}
}

func printReviewCard(w io.Writer, index, total int, it item.Item) {
	fmt.Fprintf(w, "\n[%d/%d] %s  %s  %s  lane=%s\n", index, total, it.ID, it.Kind, it.State, it.Lane)
	switch {
	case it.Kind == item.Proposal:
		fmt.Fprintf(w, "  target:     %s %s\n", it.EditKind, it.Target)
		if it.Repository != "" || it.File != "" {
			fmt.Fprintf(w, "  location:   %s %s\n", it.Repository, it.File)
		}
		fmt.Fprintln(w, "  change:")
		printIndented(w, it.Change)
		if it.Reason != "" {
			fmt.Fprintln(w, "  reason:")
			printIndented(w, it.Reason)
		}
	case it.State == item.Scored:
		if it.Title != "" {
			fmt.Fprintf(w, "  title:      %s\n", it.Title)
		}
		recOut := recommendedOutcome(it.Recommendation)
		fmt.Fprintf(w, "  score:      %d  (recommended: %s -> %s)\n", *it.Score, recOut, it.Recommendation)
		if it.SplitFlag {
			fmt.Fprintln(w, "  split_flag: true")
		}
	case it.State == item.ScreenedOut:
		if it.Title != "" {
			fmt.Fprintf(w, "  title:      %s\n", it.Title)
		}
		fmt.Fprintln(w, "  screened:")
		printIndented(w, it.ScreenReason)
	}
	outcomes := item.Outcomes(it.Kind, it.State)
	names := make([]string, len(outcomes))
	for i, o := range outcomes {
		names[i] = string(o)
	}
	fmt.Fprintf(w, "  outcomes:   %s\n", strings.Join(names, ", "))
}

func printIndented(w io.Writer, text string) {
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(w, "    %s\n", line)
	}
}
