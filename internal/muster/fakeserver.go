package muster

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/strictjson"
)

// maxRequest is the largest request body Muster reads.
const maxRequest = 64 << 10

// NewFakeServer serves Muster's five worker calls and its release call over
// HTTP on top of any Queue, normally a StandIn. It is for tests and for
// trying Countersign by hand. It accepts one made-up bearer token.
//
// It answers the way Muster's contract says: a refusal is a status and a
// body {"error", "cause", "reason"} with the contract's causes, an unknown
// field is refused, and every request must carry "contract": "v1".
func NewFakeServer(q Queue, token string) http.Handler {
	mux := http.NewServeMux()
	handle := func(call string, fn func(r *http.Request, req fakeRequest) (int, any)) {
		mux.HandleFunc("/v1/"+call, func(w http.ResponseWriter, r *http.Request) {
			status, body := fakeCheck(r, token)
			var req fakeRequest
			if status == 0 {
				status, body = fakeRead(r, &req)
			}
			if status == 0 {
				status, body = fn(r, req)
			}
			if body == nil {
				w.WriteHeader(status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			switch b := body.(type) {
			case []byte:
				w.Write(b)
			default:
				json.NewEncoder(w).Encode(b)
			}
		})
	}
	answer := func(err error, ok any) (int, any) {
		switch {
		case err == nil:
			return http.StatusOK, ok
		case errors.Is(err, ErrLeaseNotHeld):
			return fakeRefusal(http.StatusConflict, rule.Parse, "lease_not_held", "the lease is not held")
		case errors.Is(err, ErrRefused):
			return fakeRefusal(http.StatusConflict, rule.Parse, "move_not_allowed", "that move is not allowed")
		}
		return fakeRefusal(http.StatusServiceUnavailable, rule.Timeout, "busy", "too many calls at once")
	}
	malformed := func() (int, any) {
		return fakeRefusal(http.StatusBadRequest, rule.Parse, "missing_field", "a required field is missing or empty")
	}

	handle("lease", func(r *http.Request, req fakeRequest) (int, any) {
		if req.ID != nil || req.Lease != nil || req.State != nil || req.Result != nil || req.Code != nil {
			return malformed()
		}
		body, err := q.Lease(r.Context())
		if err == nil && body == nil {
			return http.StatusNoContent, nil
		}
		return answer(err, body)
	})
	handle("heartbeat", func(r *http.Request, req fakeRequest) (int, any) {
		if req.Lease == nil || req.State != nil || req.Result != nil || req.Code != nil {
			return malformed()
		}
		return answer(q.Heartbeat(r.Context(), *req.Lease), map[string]string{"lease": *req.Lease})
	})
	handle("complete", func(r *http.Request, req fakeRequest) (int, any) {
		if req.Lease == nil || req.State == nil || req.Code != nil {
			return malformed()
		}
		return answer(q.Complete(r.Context(), *req.Lease, item.State(*req.State), req.Result), map[string]string{"state": *req.State})
	})
	handle("fail", func(r *http.Request, req fakeRequest) (int, any) {
		if req.Lease == nil || req.Code == nil || req.State != nil || req.Result != nil {
			return malformed()
		}
		return answer(q.Fail(r.Context(), *req.Lease, rule.Code(*req.Code)), map[string]string{})
	})
	handle("needs_subtool", func(r *http.Request, req fakeRequest) (int, any) {
		if req.Lease == nil || req.State != nil || req.Result != nil || req.Code != nil {
			return malformed()
		}
		return answer(q.NeedsSubtool(r.Context(), *req.Lease), map[string]string{})
	})
	handle("release", func(r *http.Request, req fakeRequest) (int, any) {
		if req.ID == nil || req.Lease != nil || req.State != nil || req.Result != nil || req.Code != nil {
			return malformed()
		}
		return answer(q.Release(r.Context(), *req.ID), map[string]string{"id": *req.ID})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		status, body := fakeRefusal(http.StatusNotFound, rule.Parse, "no_such_call", "no such call")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	})
	return mux
}

type fakeRequest struct {
	Contract *string         `json:"contract"`
	ID       *string         `json:"id"`
	Lease    *string         `json:"lease"`
	State    *string         `json:"state"`
	Result   json.RawMessage `json:"result"`
	Code     *string         `json:"code"`
}

func fakeRefusal(status int, code rule.Code, cause, reason string) (int, any) {
	return status, map[string]string{"error": string(code), "cause": cause, "reason": reason}
}

// fakeCheck applies the checks that come before the body is read.
func fakeCheck(r *http.Request, token string) (int, any) {
	if r.Method != http.MethodPost {
		return fakeRefusal(http.StatusMethodNotAllowed, rule.Parse, "wrong_method", "this call does not accept that method")
	}
	if got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); !ok || got != token {
		return fakeRefusal(http.StatusUnauthorized, rule.Parse, "no_credential", "no valid credential was presented")
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return fakeRefusal(http.StatusUnsupportedMediaType, rule.Parse, "wrong_type", "the content type must be application/json")
	}
	return 0, nil
}

// fakeRead reads the request body, refusing an unknown field and a contract
// version other than v1.
func fakeRead(r *http.Request, req *fakeRequest) (int, any) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequest+1))
	if err != nil {
		return fakeRefusal(http.StatusBadRequest, rule.Parse, "not_json", "the request could not be read")
	}
	if len(body) > maxRequest {
		return fakeRefusal(http.StatusRequestEntityTooLarge, rule.Parse, "too_large", "the request is too large")
	}
	if strictjson.Decode(body, req) != nil || req.Contract == nil {
		return fakeRefusal(http.StatusBadRequest, rule.Parse, "bad_field", "a required field is missing or malformed")
	}
	if *req.Contract != ContractVersion {
		return fakeRefusal(http.StatusUnprocessableEntity, rule.NeedsSubtool, "unknown_contract", "this contract version is not known")
	}
	return 0, nil
}
