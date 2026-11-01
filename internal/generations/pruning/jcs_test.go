//go:build unit

package pruning

import (
	"errors"
	"math"
	"testing"
)

func TestCanonicalSortsPropertiesByUTF16(t *testing.T) {
	// RFC 8785 section 3.2.3's sorting test data.
	v, err := Parse([]byte(`{"\u20ac":"Euro Sign","\r":"Carriage Return","\ufb33":"Hebrew Letter Dalet With Dagesh","1":"One",` +
		`"\ud83d\ude00":"Emoji: Grinning Face","\u0080":"Control","\u00f6":"Latin Small Letter O With Diaeresis"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"\\r\":\"Carriage Return\",\"1\":\"One\",\"\u0080\":\"Control\",\"\u00f6\":\"Latin Small Letter O With Diaeresis\"," +
		"\"\u20ac\":\"Euro Sign\",\"\U0001F600\":\"Emoji: Grinning Face\",\"\ufb33\":\"Hebrew Letter Dalet With Dagesh\"}"
	if got := string(v.Canonical()); got != want {
		t.Fatalf("canonical = %s\nwant      %s", got, want)
	}
}

func TestNumbersSerializeAsAppendixB(t *testing.T) {
	for bits, want := range map[uint64]string{
		0x0000000000000000: "0", 0x8000000000000000: "0",
		0x0000000000000001: "5e-324", 0x8000000000000001: "-5e-324",
		0x7fefffffffffffff: "1.7976931348623157e+308", 0xffefffffffffffff: "-1.7976931348623157e+308",
		0x4340000000000000: "9007199254740992", 0xc340000000000000: "-9007199254740992",
		0x4430000000000000: "295147905179352830000",
		0x44b52d02c7e14af5: "9.999999999999997e+22", 0x44b52d02c7e14af6: "1e+23", 0x44b52d02c7e14af7: "1.0000000000000001e+23",
		0x444b1ae4d6e2ef4e: "999999999999999700000", 0x444b1ae4d6e2ef4f: "999999999999999900000", 0x444b1ae4d6e2ef50: "1e+21",
		0x3eb0c6f7a0b5ed8c: "9.999999999999997e-7", 0x3eb0c6f7a0b5ed8d: "0.000001",
		0x41b3de4355555553: "333333333.3333332", 0x41b3de4355555554: "333333333.33333325", 0x41b3de4355555555: "333333333.3333333",
		0x41b3de4355555556: "333333333.3333334", 0x41b3de4355555557: "333333333.33333343",
		0xbecbf647612f3696: "-0.0000033333333333333333",
		0x43143ff3c1cb0959: "1424953923781206.2",
	} {
		if got := formatNumber(math.Float64frombits(bits)); got != want {
			t.Errorf("%016x = %s, want %s", bits, got, want)
		}
	}
}

func TestParseRefusesWhatCanonicalizationWouldAlter(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate key":     `{"a":1,"a":2}`,
		"lone surrogate":    `{"a":"\ud800"}`,
		"lone low half":     `{"a":"\udc00x"}`,
		"invalid UTF-8":     "{\"a\":\"\xff\"}",
		"large integer":     `{"a":12345678901234567890}`,
		"altered fraction":  `{"a":0.10000000000000001}`,
		"out of range":      `{"a":1e400}`,
		"underflow":         `{"a":1e-400}`,
		"raw control":       "{\"a\":\"\x01\"}",
		"not JSON":          `{"a":`,
		"trailing data":     `{} {}`,
		"leading zero":      `{"a":01}`,
		"bare decimal mark": `{"a":1.}`,
	} {
		if _, err := Parse([]byte(body)); !errors.Is(err, ErrNotCanonical) {
			t.Errorf("%s: err = %v, want ErrNotCanonical", name, err)
		}
	}
}

func TestParseKeepsValuesCanonicalizationPreserves(t *testing.T) {
	v, err := Parse([]byte(`{"b":[4.50,1E30,2e-3,-0,9007199254740991],"a":"\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"a\":\"\u20ac$\\u000f\\nA'B\\\"\\\\\\\\\\\"/\",\"b\":[4.5,1e+30,0.002,0,9007199254740991]}"
	if got := string(v.Canonical()); got != want {
		t.Fatalf("canonical = %s\nwant      %s", got, want)
	}
}
