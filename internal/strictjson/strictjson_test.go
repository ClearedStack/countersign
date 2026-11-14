package strictjson_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ClearedStack/countersign/internal/strictjson"
)

func TestObject(t *testing.T) {
	deep := strings.Repeat(`{"a":`, strictjson.MaxDepth) + `1` + strings.Repeat(`}`, strictjson.MaxDepth)
	tooDeep := strings.Repeat(`{"a":`, strictjson.MaxDepth+1) + `1` + strings.Repeat(`}`, strictjson.MaxDepth+1)

	tests := []struct {
		name string
		in   string
		want string // empty means refused
	}{
		{name: "plain object", in: `{"a":1}`, want: `{"a":1}`},
		{name: "white space is removed", in: " {\n \"a\" : [1, 2] }\n", want: `{"a":[1,2]}`},
		{name: "empty object", in: `{}`, want: `{}`},
		{name: "html characters are not escaped", in: `{"a":"<b>&"}`, want: `{"a":"<b>&"}`},
		{name: "deepest nesting allowed", in: deep, want: deep},
		{name: "nested too deeply", in: tooDeep},
		{name: "empty input", in: ``},
		{name: "not json", in: `{"a":`},
		{name: "an array", in: `[{"a":1}]`},
		{name: "a string", in: `"a"`},
		{name: "null", in: `null`},
		{name: "two objects", in: `{"a":1}{"b":2}`},
		{name: "data after the object", in: `{"a":1} x`},
		{name: "duplicate key", in: `{"a":1,"a":2}`},
		{name: "duplicate key after escape", in: `{"a":1,"a":2}`},
		{name: "duplicate key when nested", in: `{"a":{"b":1,"b":2}}`},
		{name: "duplicate key in an array element", in: `{"a":[{"b":1,"b":2}]}`},
		{name: "same key in different objects", in: `{"a":{"b":1},"c":{"b":2}}`, want: `{"a":{"b":1},"c":{"b":2}}`},
		{name: "invalid utf-8", in: "{\"a\":\"\xff\"}"},
		{name: "byte order mark", in: "\xef\xbb\xbf{}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := strictjson.Object([]byte(tt.in))
			if tt.want == "" {
				if err == nil {
					t.Fatalf("Object(%q) = %q, want a refusal", tt.in, got)
				}
				if err != strictjson.ErrNotStrict {
					t.Fatalf("Object(%q) error = %v, want ErrNotStrict", tt.in, err)
				}
				return
			}
			if err != nil || string(got) != tt.want {
				t.Fatalf("Object(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
	}
}

func FuzzObject(f *testing.F) {
	for _, s := range []string{`{}`, `{"a":1}`, `{"a":1,"a":2}`, `[]`, ``, `{"a":{"b":[1,{"c":null}]}}`, "{\"a\":\"\xff\"}"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := strictjson.Object(data)
		if err != nil {
			if err != strictjson.ErrNotStrict {
				t.Fatalf("unexpected error %v", err)
			}
			return
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatalf("accepted a value that is not an object: %q", got)
		}
		again, err := strictjson.Object(got)
		if err != nil || !bytes.Equal(again, got) {
			t.Fatalf("result is not a fixed point: %q then %q, %v", got, again, err)
		}
	})
}
