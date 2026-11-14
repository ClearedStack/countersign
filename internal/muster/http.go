package muster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/rule"
)

// DefaultTokenFile is where a pod finds its ServiceAccount token.
const DefaultTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// maxToken bounds the token file. A ServiceAccount token is a few kilobytes.
const maxToken = 16 << 10

// maxOtherBody is how much of any answer other than a lease is read.
const maxOtherBody = 16 << 10

// ErrConfig is returned when the address of Muster is not usable. It does
// not repeat the address.
var ErrConfig = errors.New("muster: the address must be a plain http or https URL")

// HTTP is the client for a real Muster. It makes the five worker calls and
// the release call as JSON over HTTP, and presents the pod's ServiceAccount
// token.
//
// Its errors are ErrRefused, ErrLeaseNotHeld and ErrUnreachable and nothing
// else. Whatever
// went wrong underneath is dropped, because it names an address.
type HTTP struct {
	base      string
	tokenFile string
	client    *http.Client
}

// NewHTTP returns a client for the Muster at baseURL. tokenFile is read
// afresh for every call, because the cluster replaces the token while the
// pod runs.
func NewHTTP(baseURL, tokenFile string) (*HTTP, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrConfig
	}
	if tokenFile == "" {
		return nil, ErrConfig
	}
	return &HTTP{
		base:      strings.TrimSuffix(u.String(), "/"),
		tokenFile: tokenFile,
		client: &http.Client{
			Transport: &http.Transport{
				// Muster is inside the cluster. A proxy named in the
				// environment is never used.
				Proxy:                  nil,
				MaxIdleConns:           2,
				MaxIdleConnsPerHost:    2,
				IdleConnTimeout:        90 * time.Second,
				TLSHandshakeTimeout:    10 * time.Second,
				ResponseHeaderTimeout:  30 * time.Second,
				MaxResponseHeaderBytes: 16 << 10,
			},
			// A redirect would carry the token to wherever it points. None
			// is followed: the redirect itself is the answer, and it is not
			// a usable one.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// token reads the ServiceAccount token as it is now.
func (h *HTTP) token() (string, bool) {
	f, err := os.Open(h.tokenFile)
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxToken+1))
	if err != nil || len(b) > maxToken {
		return "", false
	}
	t := strings.TrimSpace(string(b))
	if t == "" || strings.ContainsAny(t, " \t\r\n") {
		return "", false
	}
	return t, true
}

type request struct {
	Contract string          `json:"contract"`
	ID       string          `json:"id,omitempty"`
	Lease    string          `json:"lease,omitempty"`
	State    item.State      `json:"state,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Code     rule.Code       `json:"code,omitempty"`
}

// post makes one call. It returns the status and up to limit bytes of the
// body. Any failure to get an answer is ErrUnreachable.
func (h *HTTP) post(ctx context.Context, call string, req request, limit int64) (int, []byte, error) {
	token, ok := h.token()
	if !ok {
		return 0, nil, ErrUnreachable // never call without a credential
	}
	req.Contract = ContractVersion
	body, err := json.Marshal(req)
	if err != nil {
		return 0, nil, ErrUnreachable
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, h.base+"/v1/"+call, bytes.NewReader(body))
	if err != nil {
		return 0, nil, ErrUnreachable
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	hr.Header.Set("Authorization", "Bearer "+token)
	resp, err := h.client.Do(hr)
	if err != nil {
		return 0, nil, ErrUnreachable
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return 0, nil, ErrUnreachable
	}
	return resp.StatusCode, got, nil
}

// sortAnswer turns an answer that is not a success into one of the three errors.
//
//   - 429 and every 5xx say try later, and so does anything that is not an
//     answer from Muster at all, such as a redirect: ErrUnreachable.
//   - 409 with the cause lease_not_held: ErrLeaseNotHeld.
//   - Every other status from 400 to 422 is a refusal: ErrRefused.
//
// The cause is one of Muster's fixed words. Nothing else from the body is
// read, and nothing from it reaches an error.
func sortAnswer(status int, body []byte) error {
	if status < 400 || status > 422 {
		return ErrUnreachable
	}
	if status == http.StatusConflict {
		var refusal struct {
			Cause string `json:"cause"`
		}
		if json.Unmarshal(body, &refusal) == nil && refusal.Cause == "lease_not_held" {
			return ErrLeaseNotHeld
		}
	}
	return ErrRefused
}

// Lease implements Queue.
func (h *HTTP) Lease(ctx context.Context) ([]byte, error) {
	status, body, err := h.post(ctx, "lease", request{}, MaxLeaseBody)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		if len(body) == 0 {
			return nil, ErrUnreachable
		}
		// A body over the limit is passed on one byte too long, so that the
		// check on arrival reports it as too large.
		return body, nil
	case http.StatusNoContent:
		return nil, nil
	}
	return nil, sortAnswer(status, nil)
}

// send makes a call whose answer matters only as yes, no or try again.
func (h *HTTP) send(ctx context.Context, call string, req request) error {
	status, body, err := h.post(ctx, call, req, maxOtherBody)
	switch {
	case err != nil:
		return err
	case status == http.StatusOK:
		return nil
	}
	return sortAnswer(status, body)
}

// Heartbeat implements Queue.
func (h *HTTP) Heartbeat(ctx context.Context, lease string) error {
	return h.send(ctx, "heartbeat", request{Lease: lease})
}

// Complete implements Queue.
func (h *HTTP) Complete(ctx context.Context, lease string, state item.State, result []byte) error {
	return h.send(ctx, "complete", request{Lease: lease, State: state, Result: result})
}

// Fail implements Queue.
func (h *HTTP) Fail(ctx context.Context, lease string, code rule.Code) error {
	return h.send(ctx, "fail", request{Lease: lease, Code: code})
}

// NeedsSubtool implements Queue.
func (h *HTTP) NeedsSubtool(ctx context.Context, lease string) error {
	return h.send(ctx, "needs_subtool", request{Lease: lease})
}

// Release implements Queue.
func (h *HTTP) Release(ctx context.Context, id string) error {
	return h.send(ctx, "release", request{ID: id})
}
