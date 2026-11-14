package muster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/rule"
)

const contractDir = "../../testdata/contract"

// exampleCall is one HTTP call in a saved contract example.
type exampleCall struct {
	As     string          `json:"as"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"`
}

// example is one of Muster's saved contract examples.
type example struct {
	About string `json:"about"`
	Given struct {
		Calls []exampleCall `json:"calls"`
	} `json:"given"`
	Request exampleCall `json:"request"`
	Expect  struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	} `json:"expect"`
}

// leaseRules says which rule Countersign applies to each step Muster's
// examples hand out. Most are other workers' steps, which Countersign has no
// rule for.
var leaseRules = map[string]rule.ID{
	"lease.json":                  rule.IntakeStateUnfamiliar, // Assay's step: an observed item
	"lease-check.json":            rule.IntakeStateUnfamiliar, // Tidegauge's step: a live item
	"lease-rebuild.json":          rule.IntakeStateUnfamiliar, // Waybill's step: an item being rebuilt
	"lease-incident.json":         rule.IntakeKindUnfamiliar,  // Bellbuoy's step
	"lease-proposal-results.json": rule.IntakeStateUnfamiliar, // Bellbuoy's step: an approved proposal
	"lease-proposal.json":         rule.IntakeAccept,
}

// notWorkerCalls counts the examples whose main request is a call Countersign
// never makes (/v1/edits, /v1/items, /v1/health, /v1/feed, /healthz).
const notWorkerCalls = 10

func compact(t *testing.T, raw []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	return buf.String()
}

// serve answers every request with status and respBody, checks the worker's
// headers, and keeps the last request line and body it was sent.
func serve(t *testing.T, status int, respBody json.RawMessage) (*httptest.Server, *string) {
	t.Helper()
	var last string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+madeUpToken {
			t.Errorf("Authorization = %q, want Bearer token", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		b, _ := io.ReadAll(r.Body)
		last = r.Method + " " + r.URL.Path + " " + string(b)
		if len(respBody) > 0 {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		w.Write(respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

// sendable makes the call a request describes, if it is one the client can
// make at all. A request with an unknown field, a missing field or a body
// that is not JSON is one the client cannot produce.
func sendable(h *HTTP, req exampleCall) (call func(context.Context) ([]byte, error), ok bool) {
	var body struct {
		Contract *string         `json:"contract"`
		ID       *string         `json:"id"`
		Lease    *string         `json:"lease"`
		State    *string         `json:"state"`
		Result   json.RawMessage `json:"result"`
		Code     *string         `json:"code"`
	}
	dec := json.NewDecoder(bytes.NewReader(req.Body))
	dec.DisallowUnknownFields()
	if req.Method != "POST" || dec.Decode(&body) != nil || body.Contract == nil || *body.Contract != ContractVersion {
		return nil, false
	}
	none := func(fn func(context.Context) error) func(context.Context) ([]byte, error) {
		return func(ctx context.Context) ([]byte, error) { return nil, fn(ctx) }
	}
	has := func(lease, state, result, code bool) bool {
		return body.ID == nil && (body.Lease != nil) == lease && (body.State != nil) == state &&
			(body.Result != nil || !result) && (body.Result == nil || result) && (body.Code != nil) == code
	}
	switch req.Path {
	case "/v1/lease":
		if has(false, false, false, false) {
			return h.Lease, true
		}
	case "/v1/heartbeat":
		if has(true, false, false, false) {
			return none(func(ctx context.Context) error { return h.Heartbeat(ctx, *body.Lease) }), true
		}
	case "/v1/complete":
		if has(true, true, body.Result != nil, false) {
			return none(func(ctx context.Context) error {
				return h.Complete(ctx, *body.Lease, item.State(*body.State), body.Result)
			}), true
		}
	case "/v1/fail":
		if has(true, false, false, true) {
			return none(func(ctx context.Context) error { return h.Fail(ctx, *body.Lease, rule.Code(*body.Code)) }), true
		}
	case "/v1/needs_subtool":
		if has(true, false, false, false) {
			return none(func(ctx context.Context) error { return h.NeedsSubtool(ctx, *body.Lease) }), true
		}
	case "/v1/release":
		if body.ID != nil && body.Lease == nil && body.State == nil && body.Result == nil && body.Code == nil {
			return none(func(ctx context.Context) error { return h.Release(ctx, *body.ID) }), true
		}
	}
	return nil, false
}

func TestContractExamples(t *testing.T) {
	names, err := filepath.Glob(filepath.Join(contractDir, "*.json"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no contract examples: %v", err)
	}
	sort.Strings(names)
	workerPaths := map[string]bool{
		"/v1/lease": true, "/v1/heartbeat": true, "/v1/complete": true,
		"/v1/fail": true, "/v1/needs_subtool": true, "/v1/release": true,
	}
	otherPaths := map[string]bool{
		"/v1/edits": true, "/v1/items": true, "/v1/health": true,
		"/v1/feed": true, "/healthz": true,
	}
	tokenPath := tokenFile(t, madeUpToken)
	ctx := context.Background()
	skipped, sent, givenSent, leased := 0, 0, 0, map[string]bool{}

	for _, name := range names {
		file := filepath.Base(name)
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var ex example
		if err := json.Unmarshal(raw, &ex); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		isRefusal := strings.HasPrefix(file, "refused-")
		if isRefusal != (ex.Expect.Status >= 400) {
			t.Errorf("%s: named as a refusal or not, but answers %d", file, ex.Expect.Status)
		}

		t.Run(file, func(t *testing.T) {
			// Replay every worker call in given.calls first, including those
			// in examples whose final request is for another endpoint.
			for i, gc := range ex.Given.Calls {
				if !workerPaths[gc.Path] {
					if !otherPaths[gc.Path] {
						t.Errorf("given.calls[%d]: unknown path %s", i, gc.Path)
					}
					continue
				}
				status, resp := 200, json.RawMessage(`{"ok":true}`)
				if gc.Path == "/v1/lease" {
					status, resp = 204, nil
				}
				gSrv, gLast := serve(t, status, resp)
				gClient := client(t, gSrv.URL, tokenPath)
				gCall, ok := sendable(gClient, gc)
				if !ok {
					t.Fatalf("given.calls[%d] (%s %s) could not be sent by the client", i, gc.Method, gc.Path)
				}
				givenSent++
				if _, err := gCall(ctx); err != nil {
					t.Errorf("given.calls[%d]: %v", i, err)
				}
				if got, wantSent := *gLast, "POST "+gc.Path+" "+compact(t, gc.Body); got != wantSent {
					t.Errorf("given.calls[%d] sent\n  %s\nwant\n  %s", i, got, wantSent)
				}
			}

			if !workerPaths[ex.Request.Path] && !isRefusal {
				if !otherPaths[ex.Request.Path] {
					t.Errorf("unknown non-worker path %s", ex.Request.Path)
				}
				skipped++
				return
			}

			srv, last := serve(t, ex.Expect.Status, ex.Expect.Body)
			h := client(t, srv.URL, tokenPath)

			// What the answer means to the client, whatever call it is on.
			var refusal struct{ Error, Cause, Reason string }
			if isRefusal {
				if err := json.Unmarshal(ex.Expect.Body, &refusal); err != nil || refusal.Error == "" || refusal.Cause == "" || refusal.Reason == "" {
					t.Fatalf("a refusal must have error, cause and reason: %s", ex.Expect.Body)
				}
			}
			var want error
			switch st := ex.Expect.Status; {
			case st == 200 || (st == 204 && ex.Request.Path == "/v1/lease"):
			case st == 409 && refusal.Cause == "lease_not_held":
				want = ErrLeaseNotHeld
			case st >= 400 && st <= 422:
				want = ErrRefused
			default:
				want = ErrUnreachable // 429 and 5xx: try again
			}

			if isRefusal {
				// The same answer on each call the client makes.
				for call, err := range map[string]error{
					"heartbeat":     h.Heartbeat(ctx, "PA-0001.1.1"),
					"complete":      h.Complete(ctx, "PA-0001.1.1", item.Approved, []byte(`{}`)),
					"fail":          h.Fail(ctx, "PA-0001.1.1", rule.Parse),
					"needs_subtool": h.NeedsSubtool(ctx, "PA-0001.1.1"),
					"release":       h.Release(ctx, "PA-0001"),
				} {
					if !errors.Is(err, want) {
						t.Errorf("%d %s on %s: got %v, want %v", ex.Expect.Status, refusal.Cause, call, err, want)
					}
					if strings.Contains(err.Error(), refusal.Reason) {
						t.Errorf("the error repeats what Muster said: %v", err)
					}
				}
				wantLease := want
				if errors.Is(want, ErrLeaseNotHeld) {
					wantLease = ErrRefused // a lease call names no lease
				}
				if _, err := h.Lease(ctx); !errors.Is(err, wantLease) {
					t.Errorf("%d on lease: got %v, want %v", ex.Expect.Status, err, wantLease)
				}
			}

			call, ok := sendable(h, ex.Request)
			if !ok {
				return
			}
			sent++
			body, err := call(ctx)
			if !errors.Is(err, want) || (want == nil && err != nil) {
				t.Errorf("the client made of %d: %v, want %v", ex.Expect.Status, err, want)
			}
			if got, wantSent := *last, "POST "+ex.Request.Path+" "+compact(t, ex.Request.Body); got != wantSent {
				t.Errorf("the client sent\n  %s\nand the contract's example is\n  %s", got, wantSent)
			}

			if ex.Request.Path != "/v1/lease" || ex.Expect.Status != 200 {
				return
			}
			leased[file] = true
			wantRule, known := leaseRules[file]
			if !known {
				t.Fatalf("a lease example with no expected rule: add %s to leaseRules", file)
			}
			if compact(t, body) != compact(t, ex.Expect.Body) {
				t.Errorf("the client changed the lease body")
			}
			l, perr := DecodeLease(body)
			var it item.Item
			if perr == nil {
				it, perr = l.Item()
			}
			got := rule.IntakeAccept
			if perr != nil {
				got = perr.Rule
			}
			if got != wantRule {
				t.Errorf("Countersign applies %s, want %s", got, wantRule)
			}
			if l.Token == "" {
				t.Errorf("the lease token was not read, so the step could not be ended")
			}
			if file == "lease-proposal.json" {
				if it.Kind != item.Proposal || it.EditKind != "DATA" || it.Reason != "It reads as if no rule applies." || l.Token != "EP-0001.1.1" {
					t.Errorf("proposal: %+v under %q", it, l.Token)
				}
			}
		})
	}

	for file := range leaseRules {
		if !leased[file] {
			t.Errorf("%s is expected and was not replayed", file)
		}
	}
	if skipped != notWorkerCalls {
		t.Errorf("%d examples are for calls Countersign does not make, expected %d: look at what changed", skipped, notWorkerCalls)
	}
	if givenSent == 0 {
		t.Error("no worker calls in given.calls were replayed")
	}
	t.Logf("%d examples: %d main calls and %d setup calls replayed, %d non-worker calls", len(names), sent, givenSent, skipped)
}
