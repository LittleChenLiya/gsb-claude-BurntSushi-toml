package toml

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func readDocFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/document.toml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// diffDocs reports every differing line with its line number, so a failing
// test points straight at the unexpected change.
func diffDocs(t *testing.T, want, got string) {
	t.Helper()
	if want == got {
		return
	}
	w := strings.SplitAfter(want, "\n")
	g := strings.SplitAfter(got, "\n")
	var b strings.Builder
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			fmt.Fprintf(&b, "  line %d:\n    want %q\n    got  %q\n", i+1, wl, gl)
		}
	}
	t.Errorf("document diff (-want +got):\n%s", b.String())
}

func TestDocumentFixtureEdit(t *testing.T) {
	orig := readDocFixture(t)

	doc, err := ParseDocument([]byte(orig))
	if err != nil {
		t.Fatal(err)
	}
	// Second [[servers]] occurrence, and a quoted key containing a dot.
	if err := doc.Set([]string{"servers", "1", "port"}, 9001); err != nil {
		t.Fatal(err)
	}
	newVal := "new \"value\"\nline"
	if err := doc.Set([]string{"quoted.dotted.key"}, newVal); err != nil {
		t.Fatal(err)
	}
	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	// Only the two target tokens may change; every other byte (comments,
	// CRLF, hex literal, multiline string, inline table) stays as-is.
	want := strings.Replace(orig, "port = 8002", "port = 9001", 1)
	want = strings.Replace(want, "\"old value\"", "\"new \\\"value\\\"\\nline\"", 1)
	diffDocs(t, want, string(out))

	if c, w := strings.Count(string(out), "\r\n"), strings.Count(orig, "\r\n"); c != w {
		t.Errorf("CRLF count changed: have %d, want %d", c, w)
	}

	var m map[string]any
	if _, err := Decode(string(out), &m); err != nil {
		t.Fatalf("edited document does not decode: %s", err)
	}
	servers := m["servers"].([]map[string]any)
	if got := servers[1]["port"]; got != int64(9001) {
		t.Errorf("servers[1].port = %v (%T), want 9001", got, got)
	}
	if got := servers[0]["port"]; got != int64(8001) {
		t.Errorf("servers[0].port = %v (%T), want 8001 (untouched)", got, got)
	}
	if got := m["quoted.dotted.key"]; got != newVal {
		t.Errorf("quoted.dotted.key = %q, want %q", got, newVal)
	}
}

func TestDocumentMultilineStringAndInlineTable(t *testing.T) {
	orig := readDocFixture(t)

	doc, err := ParseDocument([]byte(orig))
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Set([]string{"description"}, "one \"line\""); err != nil {
		t.Fatal(err)
	}
	if err := doc.Set([]string{"servers", "1", "meta", "retries"}, 5); err != nil {
		t.Fatal(err)
	}
	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	oldML := "\"\"\"\r\nLine one.\r\nLine \"two\" with quotes.\r\n\"\"\""
	if !strings.Contains(orig, oldML) {
		t.Fatalf("fixture does not contain expected multiline string")
	}
	want := strings.Replace(orig, oldML, "\"one \\\"line\\\"\"", 1)
	want = strings.Replace(want, "retries = 3", "retries = 5", 1)
	diffDocs(t, want, string(out))

	var m map[string]any
	if _, err := Decode(string(out), &m); err != nil {
		t.Fatal(err)
	}
	if got := m["description"]; got != "one \"line\"" {
		t.Errorf("description = %q, want %q", got, "one \"line\"")
	}
	meta := m["servers"].([]map[string]any)[1]["meta"].(map[string]any)
	if got := meta["retries"]; got != int64(5) {
		t.Errorf("meta.retries = %v, want 5", got)
	}
}

func TestDocumentArrayElements(t *testing.T) {
	orig := readDocFixture(t)

	doc, err := ParseDocument([]byte(orig))
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Set([]string{"ports", "0"}, 9000); err != nil {
		t.Fatal(err)
	}
	if err := doc.Set([]string{"servers", "0", "tags", "1"}, "z"); err != nil {
		t.Fatal(err)
	}
	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	want := strings.Replace(orig, "ports = [8000, 8001, 8002]", "ports = [9000, 8001, 8002]", 1)
	want = strings.Replace(want, "[\"a\", \"b\"]", "[\"a\", \"z\"]", 1)
	diffDocs(t, want, string(out))
}

func TestDocumentTypeChange(t *testing.T) {
	orig := readDocFixture(t)

	doc, err := ParseDocument([]byte(orig))
	if err != nil {
		t.Fatal(err)
	}
	// Hex int -> float, bool -> int, float -> string.
	if err := doc.Set([]string{"max_connections"}, 7.5); err != nil {
		t.Fatal(err)
	}
	if err := doc.Set([]string{"enabled"}, 42); err != nil {
		t.Fatal(err)
	}
	if err := doc.Set([]string{"ratio"}, "half"); err != nil {
		t.Fatal(err)
	}
	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	if _, err := Decode(string(out), &m); err != nil {
		t.Fatalf("edited document does not decode: %s", err)
	}
	if got := m["max_connections"]; got != 7.5 {
		t.Errorf("max_connections = %v (%T), want 7.5", got, got)
	}
	if got := m["enabled"]; got != int64(42) {
		t.Errorf("enabled = %v (%T), want 42", got, got)
	}
	if got := m["ratio"]; got != "half" {
		t.Errorf("ratio = %v (%T), want \"half\"", got, got)
	}
}

func TestDocumentSemanticRoundtrip(t *testing.T) {
	orig := readDocFixture(t)

	doc, err := ParseDocument([]byte(orig))
	if err != nil {
		t.Fatal(err)
	}
	edits := []struct {
		path  []string
		value any
	}{
		{[]string{"servers", "1", "port"}, 9001},
		{[]string{"quoted.dotted.key"}, "changed"},
		{[]string{"ports", "2"}, 9002},
		{[]string{"max_connections"}, 31337},
		{[]string{"title"}, "web \"tier\" v2"},
	}
	for _, e := range edits {
		if err := doc.Set(e.path, e.value); err != nil {
			t.Fatal(err)
		}
	}
	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	var want, got map[string]any
	if _, err := Decode(orig, &want); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(string(out), &got); err != nil {
		t.Fatal(err)
	}

	// Apply the same edits to the decoded original; the maps must then be
	// identical, proving nothing else changed semantically.
	want["servers"].([]map[string]any)[1]["port"] = int64(9001)
	want["quoted.dotted.key"] = "changed"
	want["ports"].([]any)[2] = int64(9002)
	want["max_connections"] = int64(31337)
	want["title"] = "web \"tier\" v2"

	if !reflect.DeepEqual(want, got) {
		t.Errorf("semantic roundtrip mismatch:\nwant %#v\ngot  %#v", want, got)
	}
}

func TestDocumentValueTypes(t *testing.T) {
	dt := time.Date(2030, 5, 6, 7, 8, 9, 123456789, time.UTC)
	cases := []struct {
		name  string
		value any
		want  any
	}{
		{"string", "plain", "plain"},
		{"string escapes", "q\"uote\nnl\ttab\\back\r\nCRLF", "q\"uote\nnl\ttab\\back\r\nCRLF"},
		{"bool", true, true},
		{"int", int(-7), int64(-7)},
		{"int64", int64(-9223372036854775808), int64(-9223372036854775808)},
		{"uint64", uint64(9223372036854775807), int64(9223372036854775807)},
		{"float64", 2.5, 2.5},
		{"float32", float32(1.5), 1.5},
		{"datetime", dt, dt},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, err := ParseDocument([]byte("k = 1\n"))
			if err != nil {
				t.Fatal(err)
			}
			if err := doc.Set([]string{"k"}, c.value); err != nil {
				t.Fatal(err)
			}
			out, err := doc.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if _, err := Decode(string(out), &m); err != nil {
				t.Fatalf("edited document does not decode: %s", err)
			}
			if want, ok := c.want.(time.Time); ok {
				got, ok := m["k"].(time.Time)
				if !ok || !got.Equal(want) {
					t.Errorf("k = %v (%T), want %v", m["k"], m["k"], want)
				}
				return
			}
			if !reflect.DeepEqual(m["k"], c.want) {
				t.Errorf("k = %#v (%T), want %#v", m["k"], m["k"], c.want)
			}
		})
	}
}

func TestDocumentErrors(t *testing.T) {
	orig := readDocFixture(t)

	t.Run("parse error", func(t *testing.T) {
		if _, err := ParseDocument([]byte("k = [1,\n")); err == nil {
			t.Fatal("expected error for invalid TOML")
		}
	})

	// Every case must fail at Set and produce no output.
	setErrs := []struct {
		name  string
		path  []string
		value any
	}{
		{"unknown key", []string{"nope"}, 1},
		{"unknown nested key", []string{"servers", "0", "nope"}, 1},
		{"unknown deep key", []string{"limits", "cpu", "x"}, 1},
		{"empty path", nil, 1},
		{"array index out of range", []string{"ports", "3"}, 1},
		{"array index negative", []string{"ports", "-1"}, 1},
		{"array index not a number", []string{"ports", "x"}, 1},
		{"array of tables out of range", []string{"servers", "2", "port"}, 1},
		{"table is not a scalar", []string{"limits"}, 1},
		{"array is not a scalar", []string{"ports"}, 1},
		{"array of tables is not a scalar", []string{"servers"}, 1},
		{"into scalar", []string{"enabled", "x"}, 1},
		{"invalid value map", []string{"enabled"}, map[string]int{"a": 1}},
		{"invalid value slice", []string{"enabled"}, []int{1}},
		{"invalid value nil", []string{"enabled"}, nil},
		{"uint64 overflow", []string{"enabled"}, uint64(18446744073709551615)},
	}
	for _, c := range setErrs {
		t.Run(c.name, func(t *testing.T) {
			doc, err := ParseDocument([]byte(orig))
			if err != nil {
				t.Fatal(err)
			}
			err = doc.Set(c.path, c.value)
			if err == nil {
				t.Fatalf("Set(%v) succeeded, want error", c.path)
			}
			var ee *EditError
			if !errors.As(err, &ee) {
				t.Fatalf("error is %T, want *EditError", err)
			}
			var buf bytes.Buffer
			if n, err := doc.WriteTo(&buf); err == nil || n != 0 || buf.Len() != 0 {
				t.Fatalf("WriteTo = (%d, %v) with %d bytes written, want no output", n, err, buf.Len())
			}
		})
	}
}

func TestDocumentConflictingEdits(t *testing.T) {
	orig := readDocFixture(t)

	doc, err := ParseDocument([]byte(orig))
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Set([]string{"enabled"}, false); err != nil {
		t.Fatal(err)
	}
	// Same token edited twice: the whole batch must be rejected.
	if err := doc.Set([]string{"enabled"}, true); err != nil {
		t.Fatal(err)
	}

	_, err = doc.Bytes()
	if err == nil {
		t.Fatal("Bytes succeeded, want conflict error")
	}
	var ee *EditError
	if !errors.As(err, &ee) {
		t.Fatalf("error is %T, want *EditError", err)
	}
	if ee.Position.Line == 0 || ee.Position.Col == 0 {
		t.Errorf("error has no line/column: %s", ee)
	}
	if !strings.Contains(ee.Error(), "line") {
		t.Errorf("error does not mention line: %q", ee.Error())
	}

	var buf bytes.Buffer
	if n, werr := doc.WriteTo(&buf); werr == nil || n != 0 || buf.Len() != 0 {
		t.Fatalf("WriteTo = (%d, %v) with %d bytes written, want no output", n, werr, buf.Len())
	}
}

func TestDocumentNoEdits(t *testing.T) {
	orig := readDocFixture(t)

	doc, err := ParseDocument([]byte(orig))
	if err != nil {
		t.Fatal(err)
	}
	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != orig {
		t.Error("document with no edits changed")
	}
}

func TestDocumentDecodeMethod(t *testing.T) {
	doc, err := ParseDocument([]byte("a = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Set([]string{"a"}, 2); err != nil {
		t.Fatal(err)
	}
	var v struct {
		A int
	}
	if _, err := doc.Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.A != 2 {
		t.Errorf("a = %d, want 2", v.A)
	}
}
