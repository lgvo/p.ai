package strictjson

import (
	"encoding/json"
	"testing"
)

type decodeCustom struct{ Seen bool }

func (c *decodeCustom) UnmarshalJSON(data []byte) error { c.Seen = len(data) > 0; return nil }

func TestDecodeExactTagsRecursively(t *testing.T) {
	type nested struct {
		Name string `json:"name"`
	}
	type payload struct {
		V      int               `json:"v"`
		Nested nested            `json:"nested"`
		List   []nested          `json:"list"`
		Values map[string]nested `json:"values"`
		Raw    json.RawMessage   `json:"raw"`
		Custom decodeCustom      `json:"custom"`
	}
	for _, raw := range []string{
		`{"V":2,"v":1}`,
		`{"v":1,"Nested":{"name":"x"}}`,
		`{"v":1,"nested":{"Name":"x"}}`,
		`{"v":1,"list":[{"Name":"x"}]}`,
		`{"v":1,"values":{"Arbitrary":{"Name":"x"}}}`,
	} {
		var p payload
		if err := Decode([]byte(raw), &p); err == nil {
			t.Fatalf("accepted inexact struct field: %s", raw)
		}
	}
	var p payload
	if err := Decode([]byte(`{"v":1,"nested":{"name":"ok"},"list":[{"name":"ok"}],"values":{"MixedCase":{"name":"ok"}},"raw":{"Arbitrary":"ok"},"custom":{"Arbitrary":"ok"}}`), &p); err != nil || !p.Custom.Seen || p.Values["MixedCase"].Name != "ok" {
		t.Fatalf("rejected intentional key space: %+v %v", p, err)
	}
}
