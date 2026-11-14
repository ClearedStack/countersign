package rule

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSafeID(t *testing.T) {
	good := []string{"PA-0001", "EP-0001", "a", "A1", "x.y_z-1", strings.Repeat("a", MaxID)}
	bad := []string{"", "-PA", ".hidden", "PA 0001", "PA/0001", "../x", "PA\n0001", "PÄ-1", strings.Repeat("a", MaxID+1), "a;b", "a\x00"}
	for _, s := range good {
		if !SafeID(s) {
			t.Errorf("SafeID(%q) = false", s)
		}
	}
	for _, s := range bad {
		if SafeID(s) {
			t.Errorf("SafeID(%q) = true", s)
		}
	}
}

func TestErrorLineIsStable(t *testing.T) {
	e := Fail(DecisionOverrideReasonMissing, "PA-0002")
	const want = "E_PARSE decision.override_reason_missing PA-0002: an override needs a written reason [fp 67665956733e504d]"
	if e.Error() != want {
		t.Errorf("got  %s\nwant %s", e.Error(), want)
	}
	if again := Fail(DecisionOverrideReasonMissing, "PA-0002"); again.Fingerprint() != e.Fingerprint() {
		t.Error("the same failure gave two fingerprints")
	}
	if other := Fail(DecisionOverrideReasonMissing, "PA-0003"); other.Fingerprint() == e.Fingerprint() {
		t.Error("two items share a fingerprint")
	}
}

func TestUnsafeItemIsLeftOut(t *testing.T) {
	e := Fail(IntakeIDInvalid, "PA 0001\x1b[2J/../etc")
	if e.Item != "" || strings.Contains(e.Error(), "etc") {
		t.Errorf("an unsafe identifier was kept: %q", e.Error())
	}
}

func TestPublicDropsForeignText(t *testing.T) {
	foreign := fmt.Errorf("open /var/spool/cs/outbox: dial tcp 192.0.2.7:8443 as user %s", "svc-account")
	e := Public(foreign, StoreUnavailable, "PA-0001")
	for _, leak := range []string{"/var", "192.0.2.7", "svc-account", "dial"} {
		if strings.Contains(e.Error(), leak) {
			t.Errorf("the public error leaks %q: %s", leak, e.Error())
		}
	}
	if e.Code() != Timeout {
		t.Errorf("code = %s", e.Code())
	}
	own := Fail(DecisionMalformed, "")
	if got := Public(fmt.Errorf("wrapped: %w", own), StoreUnavailable, ""); got != own {
		t.Error("a public error did not pass through")
	}
	if Public(nil, StoreUnavailable, "") != nil {
		t.Error("nil became an error")
	}
	var target *Error
	if !errors.As(e, &target) {
		t.Error("Public did not return an *Error")
	}
}
