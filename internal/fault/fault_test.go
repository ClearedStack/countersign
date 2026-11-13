package fault

import "testing"

func TestErrorTextAndFingerprintAreFixed(t *testing.T) {
	e := New(Parse, "override-reason-missing", "an override needs a written reason")
	const want = "E_PARSE: an override needs a written reason (fingerprint 5be3008405d2)"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
	if New(Timeout, "override-reason-missing", "x").Fingerprint() == e.Fingerprint() {
		t.Fatal("a different code must give a different fingerprint")
	}
}

func TestTheThreePublicCodes(t *testing.T) {
	if Timeout != "E_TIMEOUT" || Parse != "E_PARSE" || NeedsSubtool != "E_NEEDS_SUBTOOL" {
		t.Fatal("the public error codes changed")
	}
}
