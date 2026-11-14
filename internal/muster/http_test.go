package muster

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ClearedStack/countersign/internal/decide"
	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/rule"
)

const madeUpToken = "made-up-token-for-countersign"

func tokenFile(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func client(t *testing.T, url, tokenPath string) *HTTP {
	t.Helper()
	h, err := NewHTTP(url, tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// seen is one request as the server received it.
type seen struct {
	Method, Path, Auth, ContentType, Body string
}

// recorder answers every request with a fixed status and body, and keeps
// what it was sent.
type recorder struct {
	mu     sync.Mutex
	status int
	body   string
	got    []seen
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.got = append(r.got, seen{req.Method, req.URL.Path, req.Header.Get("Authorization"), req.Header.Get("Content-Type"), string(b)})
	status, body := r.status, r.body
	r.mu.Unlock()
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func (r *recorder) requests() []seen {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seen(nil), r.got...)
}

// The whole path: the client, over HTTP, to a fake server that applies the
// contract's checks, on top of the stand-in.
func TestFiveCallsAgainstTheFakeServer(t *testing.T) {
	c := &clock{at}
	s := standIn(t, c, scoredStep, screenedStep, proposalStep)
	srv := httptest.NewServer(NewFakeServer(s, madeUpToken))
	defer srv.Close()
	h := client(t, srv.URL, tokenFile(t, madeUpToken))
	ctx := context.Background()

	lease := func() (Lease, item.Item) {
		t.Helper()
		body, err := h.Lease(ctx)
		if err != nil || body == nil {
			t.Fatalf("lease: %s %v", body, err)
		}
		l, perr := DecodeLease(body)
		if perr != nil {
			t.Fatal(perr)
		}
		it, perr := l.Item()
		if perr != nil {
			t.Fatal(perr)
		}
		return l, it
	}
	complete := func(l Lease, it item.Item, d decide.Decision) error {
		rec := decided(t, it, d)
		b, _ := json.Marshal(rec)
		return h.Complete(ctx, l.Token, rec.To, b)
	}

	l1, it1 := lease()
	l2, it2 := lease()
	l3, it3 := lease()
	if body, err := h.Lease(ctx); err != nil || body != nil {
		t.Fatalf("a fourth lease should be 204: %s %v", body, err)
	}
	if l1.Token != "PA-0001.1" || l1.Kind != "item" || l1.State != "scored" || l1.Attempt != 1 || l1.Expires != "2026-01-05T10:01:00Z" {
		t.Errorf("first lease: %+v", l1)
	}
	if it3.Kind != item.Proposal || it3.EditKind != "DATA" || it3.Change != "Fix the spelling." {
		t.Errorf("proposal: %+v", it3)
	}

	if err := h.Heartbeat(ctx, l1.Token); err != nil {
		t.Errorf("heartbeat: %v", err)
	}
	if err := h.Heartbeat(ctx, "PA-0001.99"); !errors.Is(err, ErrRefused) {
		t.Errorf("heartbeat on a lease never given: %v", err)
	}
	if err := h.NeedsSubtool(ctx, l1.Token); !errors.Is(err, ErrRefused) {
		t.Errorf("needs_subtool on a scored item: %v", err)
	}
	if err := complete(l1, it1, decide.Decision{Outcome: item.Park, Blocker: "Waiting for a made-up table."}); err != nil {
		t.Errorf("complete: %v", err)
	}
	if err := complete(l1, it1, decide.Decision{Outcome: item.Park, Blocker: "Waiting for a made-up table."}); err != nil {
		t.Errorf("the same complete again: %v", err)
	}
	if err := complete(l2, it2, decide.Decision{Outcome: item.Override, Reason: "Reshaped to do one job."}); err != nil {
		t.Errorf("override: %v", err)
	}
	if err := h.Fail(ctx, l3.Token, rule.Parse); err != nil {
		t.Errorf("fail: %v", err)
	}

	snap := s.Snapshot()
	states := map[string]string{}
	for _, it := range snap.Items {
		states[it.ID] = it.State + "/" + it.Status
	}
	want := map[string]string{"PA-0001": "parked/closed", "PA-0002": "observed/closed", "EP-0001": "pending_approval/held"}
	for id, w := range want {
		if states[id] != w {
			t.Errorf("%s is %s, want %s", id, states[id], w)
		}
	}
	if len(snap.Ledger) != 3 || snap.Ledger[0].Result == nil || snap.Ledger[0].Result.Blocker != "Waiting for a made-up table." {
		t.Errorf("ledger: %+v", snap.Ledger)
	}
}

// What goes on the wire, byte for byte.
func TestRequestsAreExactlyTheContract(t *testing.T) {
	rec := &recorder{status: 200, body: `{}`}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	h := client(t, srv.URL, tokenFile(t, madeUpToken))
	ctx := context.Background()

	h.Lease(ctx)
	h.Heartbeat(ctx, "PA-0001.1")
	h.Complete(ctx, "PA-0001.1", item.Approved, []byte(`{"outcome":"approve"}`))
	h.Fail(ctx, "PA-0001.1", rule.Parse)
	h.Fail(ctx, "PA-0001.1", rule.Timeout)
	h.NeedsSubtool(ctx, "PA-0001.1")

	want := []seen{
		{"POST", "/v1/lease", "", "", `{"contract":"v1"}`},
		{"POST", "/v1/heartbeat", "", "", `{"contract":"v1","lease":"PA-0001.1"}`},
		{"POST", "/v1/complete", "", "", `{"contract":"v1","lease":"PA-0001.1","state":"approved","result":{"outcome":"approve"}}`},
		{"POST", "/v1/fail", "", "", `{"contract":"v1","lease":"PA-0001.1","code":"E_PARSE"}`},
		{"POST", "/v1/fail", "", "", `{"contract":"v1","lease":"PA-0001.1","code":"E_TIMEOUT"}`},
		{"POST", "/v1/needs_subtool", "", "", `{"contract":"v1","lease":"PA-0001.1"}`},
	}
	got := rec.requests()
	if len(got) != len(want) {
		t.Fatalf("%d requests, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Method != w.Method || g.Path != w.Path || g.Body != w.Body {
			t.Errorf("request %d:\n got %s %s %s\nwant %s %s %s", i+1, g.Method, g.Path, g.Body, w.Method, w.Path, w.Body)
		}
		if g.Auth != "Bearer "+madeUpToken || g.ContentType != "application/json" {
			t.Errorf("request %d headers: %q %q", i+1, g.Auth, g.ContentType)
		}
	}
}

func TestTokenIsReadAfreshForEveryCall(t *testing.T) {
	rec := &recorder{status: 204}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	path := tokenFile(t, "made-up-first-token")
	h := client(t, srv.URL, path)
	ctx := context.Background()

	h.Lease(ctx)
	if err := os.WriteFile(path, []byte("made-up-second-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.Lease(ctx)
	got := rec.requests()
	if len(got) != 2 || got[0].Auth != "Bearer made-up-first-token" || got[1].Auth != "Bearer made-up-second-token" {
		t.Fatalf("tokens sent: %+v", got)
	}

	// No usable token: no call is made at all.
	for name, content := range map[string]string{"empty": "", "blank": " \n", "two words": "made-up token"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Lease(ctx); !errors.Is(err, ErrUnreachable) {
			t.Errorf("%s token: %v", name, err)
		}
	}
	os.Remove(path)
	if err := h.Heartbeat(ctx, "PA-0001.1"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("missing token file: %v", err)
	}
	if n := len(rec.requests()); n != 2 {
		t.Errorf("%d requests reached the server; calls without a token must not be sent", n)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	elsewhere := &recorder{status: 200, body: `{}`}
	other := httptest.NewServer(elsewhere)
	defer other.Close()

	for _, status := range []int{301, 302, 303, 307, 308} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+r.URL.Path, status)
		}))
		h := client(t, srv.URL, tokenFile(t, madeUpToken))
		if _, err := h.Lease(context.Background()); !errors.Is(err, ErrUnreachable) {
			t.Errorf("%d on lease: %v", status, err)
		}
		if err := h.Complete(context.Background(), "PA-0001.1", item.Approved, []byte(`{}`)); !errors.Is(err, ErrUnreachable) {
			t.Errorf("%d on complete: %v", status, err)
		}
		srv.Close()
	}
	if got := elsewhere.requests(); len(got) != 0 {
		t.Errorf("the token was carried to another server: %+v", got)
	}
}

func TestNoProxyIsEverUsed(t *testing.T) {
	proxy := &recorder{status: 200, body: `{}`}
	psrv := httptest.NewServer(proxy)
	defer psrv.Close()
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY"} {
		t.Setenv(name, psrv.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	h := client(t, "http://muster.invalid:8080", tokenFile(t, madeUpToken))
	tr, ok := h.client.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Fatal("the transport must have no proxy function at all")
	}
	// A name that does not resolve: with a proxy in use the request would
	// arrive at the proxy instead.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := h.Lease(ctx); !errors.Is(err, ErrUnreachable) {
		t.Errorf("got %v", err)
	}
	if got := proxy.requests(); len(got) != 0 {
		t.Errorf("the proxy from the environment was used: %+v", got)
	}
}

func TestStatusesAreSortedIntoYesNoAndTryAgain(t *testing.T) {
	refusal := `{"error":"E_PARSE","cause":"made-up","reason":"a made-up reason naming host 192.0.2.7"}`
	cases := []struct {
		status int
		body   string
		want   error // for a call other than lease
	}{
		{200, `{"id":"PA-0001","state":"approved","next":"waybill"}`, nil},
		{400, refusal, ErrRefused},
		{409, refusal, ErrRefused},
		{413, refusal, ErrRefused},
		{401, refusal, ErrUnreachable}, // the credential, not the call
		{403, refusal, ErrUnreachable},
		{404, refusal, ErrUnreachable},
		{405, refusal, ErrUnreachable},
		{415, refusal, ErrUnreachable},
		{422, `{"error":"E_NEEDS_SUBTOOL","cause":"made-up","reason":"made-up"}`, ErrUnreachable},
		{429, `{"error":"E_TIMEOUT","cause":"made-up","reason":"made-up"}`, ErrUnreachable},
		{500, refusal, ErrUnreachable},
		{503, `{"error":"E_TIMEOUT","cause":"made-up","reason":"made-up"}`, ErrUnreachable},
		{204, ``, ErrUnreachable}, // only a lease may be answered with nothing
	}
	for _, c := range cases {
		rec := &recorder{status: c.status, body: c.body}
		srv := httptest.NewServer(rec)
		h := client(t, srv.URL, tokenFile(t, madeUpToken))
		ctx := context.Background()
		for name, err := range map[string]error{
			"heartbeat":     h.Heartbeat(ctx, "PA-0001.1"),
			"complete":      h.Complete(ctx, "PA-0001.1", item.Approved, []byte(`{}`)),
			"fail":          h.Fail(ctx, "PA-0001.1", rule.Parse),
			"needs_subtool": h.NeedsSubtool(ctx, "PA-0001.1"),
		} {
			if !errors.Is(err, c.want) || (c.want == nil && err != nil) {
				t.Errorf("%d on %s: got %v, want %v", c.status, name, err, c.want)
			}
			if err != nil && (strings.Contains(err.Error(), "192.0.2.7") || strings.Contains(err.Error(), srv.Listener.Addr().String())) {
				t.Errorf("%d on %s: the error carries what the server said or where it is: %v", c.status, name, err)
			}
		}
		// Lease: 200 with a body, 204 for nothing, anything else try again.
		body, err := h.Lease(ctx)
		switch {
		case c.status == 200 && (err != nil || string(body) != c.body):
			t.Errorf("200 on lease: %s %v", body, err)
		case c.status == 204 && (err != nil || body != nil):
			t.Errorf("204 on lease: %s %v", body, err)
		case c.status != 200 && c.status != 204 && !errors.Is(err, ErrUnreachable):
			t.Errorf("%d on lease: %v", c.status, err)
		}
		srv.Close()
	}
}

func TestErrorsNeverNameTheAddress(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.Listener.Addr().String()
	url := srv.URL
	srv.Close() // nothing listens there now

	h := client(t, url, tokenFile(t, madeUpToken))
	_, err := h.Lease(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), addr) || strings.Contains(err.Error(), "dial") || strings.Contains(err.Error(), "refused") {
		t.Errorf("the error carries the network failure: %v", err)
	}
}

func TestACallThatHangsEndsWithItsContext(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	h := client(t, srv.URL, tokenFile(t, madeUpToken))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := h.Heartbeat(ctx, "PA-0001.1"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("the call was not cut off")
	}
}

func TestAnOversizedLeaseIsReportedNotSwallowed(t *testing.T) {
	big := `{"lease":"PA-0001.1","results":{"sextant":{"notes":"` + strings.Repeat("filler ", MaxLeaseBody/7+10) + `"}}}`
	srv := httptest.NewServer(&recorder{status: 200, body: big})
	defer srv.Close()
	h := client(t, srv.URL, tokenFile(t, madeUpToken))
	body, err := h.Lease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != MaxLeaseBody+1 {
		t.Errorf("read %d bytes; the client must stop just past the limit", len(body))
	}
	if _, perr := DecodeLease(body); perr == nil || perr.Rule != rule.IntakePayloadTooLarge {
		t.Errorf("got %v", perr)
	}
}

func TestAddressMustBeAPlainURL(t *testing.T) {
	path := tokenFile(t, madeUpToken)
	for _, bad := range []string{"", "muster:8080", "ftp://muster.invalid", "http://", "http://user:pass@muster.invalid",
		"http://muster.invalid/?x=1", "http://muster.invalid/#frag", "://nope"} {
		if _, err := NewHTTP(bad, path); !errors.Is(err, ErrConfig) {
			t.Errorf("NewHTTP(%q) = %v", bad, err)
		} else if bad != "" && strings.Contains(err.Error(), bad) {
			t.Errorf("the error repeats the address: %v", err)
		}
	}
	if _, err := NewHTTP("http://muster.invalid:8080", ""); !errors.Is(err, ErrConfig) {
		t.Errorf("no token file: %v", err)
	}
	h, err := NewHTTP("http://muster.invalid:8080/", path)
	if err != nil || h.base != "http://muster.invalid:8080" {
		t.Errorf("trailing slash: %+v %v", h, err)
	}
}

// The fake server applies the contract's checks to whoever calls it.
func TestFakeServerRefusesWhatTheContractRefuses(t *testing.T) {
	s := standIn(t, &clock{at}, scoredStep)
	srv := httptest.NewServer(NewFakeServer(s, madeUpToken))
	defer srv.Close()

	do := func(method, path, auth, contentType, body string) (int, map[string]string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]string
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	bearer, js := "Bearer "+madeUpToken, "application/json"
	cases := []struct {
		name                            string
		method, path, auth, ctype, body string
		status                          int
		code                            string
	}{
		{"no credential", "POST", "/v1/lease", "", js, `{"contract":"v1"}`, 401, "E_PARSE"},
		{"wrong credential", "POST", "/v1/lease", "Bearer someone-else", js, `{"contract":"v1"}`, 401, "E_PARSE"},
		{"wrong method", "GET", "/v1/lease", bearer, js, ``, 405, "E_PARSE"},
		{"wrong content type", "POST", "/v1/lease", bearer, "text/plain", `{"contract":"v1"}`, 415, "E_PARSE"},
		{"no such call", "POST", "/v1/release", bearer, js, `{"contract":"v1"}`, 404, "E_PARSE"},
		{"not JSON", "POST", "/v1/lease", bearer, js, `lease please`, 400, "E_PARSE"},
		{"no contract", "POST", "/v1/lease", bearer, js, `{}`, 400, "E_PARSE"},
		{"unknown field", "POST", "/v1/lease", bearer, js, `{"contract":"v1","worker":"countersign"}`, 400, "E_PARSE"},
		{"a field the call does not take", "POST", "/v1/lease", bearer, js, `{"contract":"v1","lease":"PA-0001.1"}`, 400, "E_PARSE"},
		{"heartbeat without a lease", "POST", "/v1/heartbeat", bearer, js, `{"contract":"v1"}`, 400, "E_PARSE"},
		{"complete without a state", "POST", "/v1/complete", bearer, js, `{"contract":"v1","lease":"PA-0001.1"}`, 400, "E_PARSE"},
		{"fail without a code", "POST", "/v1/fail", bearer, js, `{"contract":"v1","lease":"PA-0001.1"}`, 400, "E_PARSE"},
		{"unknown contract version", "POST", "/v1/lease", bearer, js, `{"contract":"v2"}`, 422, "E_NEEDS_SUBTOOL"},
		{"too large", "POST", "/v1/lease", bearer, js, `{"contract":"v1","lease":"` + strings.Repeat("x", 70<<10) + `"}`, 413, "E_PARSE"},
		{"lease not held", "POST", "/v1/heartbeat", bearer, js, `{"contract":"v1","lease":"PA-0001.9"}`, 409, "E_PARSE"},
	}
	for _, c := range cases {
		status, body := do(c.method, c.path, c.auth, c.ctype, c.body)
		if status != c.status || body["error"] != c.code || body["reason"] == "" || body["cause"] == "" {
			t.Errorf("%s: %d %v, want %d %s", c.name, status, body, c.status, c.code)
		}
	}
	if snap := s.Snapshot(); snap.Items[0].Status != StatusWaiting {
		t.Errorf("a refused request leased the step: %+v", snap.Items[0])
	}
}
