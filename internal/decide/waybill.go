package decide

import (
	"encoding/json"
	"strings"

	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/strictjson"
)

// specification is the shape of the specification file, as far as Waybill's
// parser reads it: ClearedStack/waybill, internal/record (Record and its
// parts), branch consort/ready-to-deploy, commit 52c51a9. Waybill refuses an
// unknown field, so this shape has none. The tool itself is left as it is:
// what it must hold depends on tool_type, and Waybill alone checks that.
type specification struct {
	ContractVersion string  `json:"contract_version"`
	ItemID          *string `json:"item_id"`
	State           *string `json:"state"`
	Determination   *struct {
		Decision           string `json:"decision"`
		OptOut             bool   `json:"opt_out"`
		NeutralDescriptor  string `json:"neutral_descriptor"`
		RequestedAt        string `json:"requested_at"`
		IdentityVerifiedAt string `json:"identity_verified_at"`
		EvaluatedAt        string `json:"evaluated_at"`
	} `json:"determination"`
	Source *struct {
		Product string `json:"product"`
		Vendor  string `json:"vendor"`
	} `json:"source"`
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	ScopeLimits []string `json:"scope_limits"`
	Authority   []struct {
		ID       string `json:"id"`
		Source   string `json:"source"`
		Citation string `json:"citation"`
		Title    string `json:"title"`
		URL      string `json:"url"`
	} `json:"authority"`
	ToolType           string          `json:"tool_type"`
	Tool               json.RawMessage `json:"tool"`
	VendorObservations json.RawMessage `json:"vendor_observations"`
}

// forbiddenKeys are the field names Waybill refuses anywhere in a result,
// because they stand for raw observations of a vendor's product. Copied from
// forbiddenObservationKeys in waybill/internal/buildspec.
var forbiddenKeys = map[string]bool{
	"html": true, "raw_html": true, "raw_text": true, "body_text": true,
	"page_text": true, "dom": true, "headers": true, "response_headers": true,
	"screenshot": true, "screenshots": true, "snapshot": true, "snapshots": true,
	"observation": true, "observations": true,
}

// ParseWaybill checks the specification file the owner attached to an
// approval and returns it with white space removed and nothing else changed.
// The file is one JSON object, within MaxWaybill bytes, in exactly the form
// Waybill's parser accepts, with the parts Waybill requires: a title, a tool
// type, a tool object and an authority with an https address. Vendor
// observations are refused when present, because nothing in them may reach
// the build specification. Whether the
// specification is correct is for Waybill and the owner to judge, not
// Countersign: nothing here reads the tool.
//
// item_id, if the file gives one, must be the item's, and state, if given,
// must be the state the decision moves the item to, because Waybill refuses
// a record that disagrees with its lease. Any failure is one fixed refusal.
func ParseWaybill(file []byte, id string, to item.State) (json.RawMessage, *rule.Error) {
	fail := rule.Fail(rule.DecisionMalformed, id)
	if len(file) == 0 || len(file) > item.MaxWaybill {
		return nil, fail
	}
	obj, err := strictjson.Object(file)
	if err != nil {
		return nil, fail
	}
	var spec specification
	if err := strictjson.Decode(obj, &spec); err != nil {
		return nil, fail
	}
	if (spec.ItemID != nil && *spec.ItemID != id) || (spec.State != nil && *spec.State != string(to)) {
		return nil, fail
	}
	if v := spec.VendorObservations; len(v) > 0 && string(v) != "null" {
		return nil, fail // free text about a vendor's product must not ride along
	}
	if spec.Title == "" || spec.ToolType == "" || len(spec.Tool) == 0 || spec.Tool[0] != '{' || !hasHTTPSAuthority(spec) {
		return nil, fail
	}
	var tree any
	if err := json.Unmarshal(obj, &tree); err != nil || hasForbiddenKey(tree) {
		return nil, fail
	}
	return obj, nil
}

// hasHTTPSAuthority reports whether the authority names at least one address
// and every address given is https.
func hasHTTPSAuthority(spec specification) bool {
	found := false
	for _, a := range spec.Authority {
		if a.URL == "" {
			continue
		}
		if !strings.HasPrefix(a.URL, "https://") {
			return false
		}
		found = true
	}
	return found
}

func hasForbiddenKey(v any) bool {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			if forbiddenKeys[k] || hasForbiddenKey(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasForbiddenKey(child) {
				return true
			}
		}
	}
	return false
}
