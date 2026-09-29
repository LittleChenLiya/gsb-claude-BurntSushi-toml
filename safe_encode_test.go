package toml

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// countWriter counts Write calls and can be set to fail.
type countWriter struct {
	calls int
	buf   bytes.Buffer
	err   error
}

func (w *countWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.err != nil {
		return 0, w.err
	}
	return w.buf.Write(p)
}

func safeEncode(t *testing.T, v any) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := NewSafeEncoder(&buf).Encode(v)
	return buf.String(), err
}

// A shared sub-object referenced from two branches is not a cycle and must
// encode normally; a value that refers back to itself is a cycle and must
// report the path. Both cases need to be told apart.
func TestSafeEncoderSharedVsCyclic(t *testing.T) {
	type inner struct {
		N int `toml:"n"`
	}
	type outer struct {
		A *inner `toml:"a"`
		B *inner `toml:"b"`
	}

	shared := &inner{N: 42}
	v := outer{A: shared, B: shared}

	have, err := safeEncode(t, v)
	if err != nil {
		t.Fatalf("shared sub-object should encode, got error: %v", err)
	}

	var want bytes.Buffer
	if err := NewEncoder(&want).Encode(v); err != nil {
		t.Fatal(err)
	}
	if have != want.String() {
		t.Errorf("output differs from Encoder:\nhave: %q\nwant: %q", have, want.String())
	}

	// Now a real cycle: a refers back to the outer value itself.
	type node struct {
		Name string `toml:"name"`
		Next *node  `toml:"next"`
	}
	n := &node{Name: "x"}
	n.Next = n

	w := new(countWriter)
	err = NewSafeEncoder(w).Encode(n)
	if err == nil {
		t.Fatal("expected cyclic reference error, got none")
	}
	if !strings.Contains(err.Error(), "cyclic reference") || !strings.Contains(err.Error(), "next") {
		t.Errorf("error should mention the cycle and the path, got: %v", err)
	}
	if w.calls != 0 {
		t.Errorf("writer was called %d times on check failure, want 0", w.calls)
	}
}

func TestSafeEncoderPointerCycle(t *testing.T) {
	type node struct {
		Next *node `toml:"next"`
	}
	a, b := &node{}, &node{}
	a.Next = b
	b.Next = a

	_, err := safeEncode(t, map[string]any{"top": a})
	if err == nil {
		t.Fatal("expected cyclic reference error, got none")
	}
	for _, want := range []string{"top", "next"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}

	// A cycle through embedded pointer fields only.
	e := &embed{}
	o := &cycleOuter{embed: e}
	e.Parent = o

	_, err = safeEncode(t, o)
	if err == nil {
		t.Fatal("expected cyclic reference error, got none")
	}
	if !strings.Contains(err.Error(), "parent") {
		t.Errorf("error should contain path 'parent', got: %v", err)
	}
}

func TestSafeEncoderMapSliceCycle(t *testing.T) {
	m := map[string]any{}
	s := []any{m}
	m["s"] = s

	_, err := safeEncode(t, m)
	if err == nil {
		t.Fatal("expected cyclic reference error, got none")
	}
	if !strings.Contains(err.Error(), "s[0]") {
		t.Errorf("error should contain path 's[0]', got: %v", err)
	}

	// Slice referring back to a map ancestor, one level deeper.
	m2 := map[string]any{}
	m2["list"] = []any{[]any{m2}}
	_, err = safeEncode(t, m2)
	if err == nil {
		t.Fatal("expected cyclic reference error, got none")
	}
	if !strings.Contains(err.Error(), "list[0][0]") {
		t.Errorf("error should contain path 'list[0][0]', got: %v", err)
	}
}

func TestSafeEncoderIgnoredCycle(t *testing.T) {
	type ignored struct {
		Visible string   `toml:"visible"`
		hidden  *ignored // unexported: never encoded
		Skipped *ignored `toml:"-"`
	}
	v := &ignored{Visible: "ok"}
	v.hidden = v
	v.Skipped = v

	have, err := safeEncode(t, v)
	if err != nil {
		t.Fatalf("cycle through ignored fields should not be reported: %v", err)
	}
	if want := "visible = \"ok\"\n"; have != want {
		t.Errorf("have: %q\nwant: %q", have, want)
	}
}

func TestSafeEncoderNil(t *testing.T) {
	type nils struct {
		P *int           `toml:"p"`
		M map[string]any `toml:"m"`
		S []string       `toml:"s"`
		I any            `toml:"i"`
	}
	have, err := safeEncode(t, nils{})
	if err != nil {
		t.Fatal(err)
	}
	if have != "" {
		t.Errorf("expected empty output for all-nil value, got %q", have)
	}
}

type safeCountMarshaler struct{ calls *int }

type embed struct {
	Parent *cycleOuter `toml:"parent"`
}

type cycleOuter struct {
	*embed
}

func (m *safeCountMarshaler) MarshalTOML() ([]byte, error) {
	*m.calls++
	return []byte(`"marshalled"`), nil
}

type safeCountTextMarshaler struct{ calls *int }

func (m *safeCountTextMarshaler) MarshalText() ([]byte, error) {
	*m.calls++
	return []byte("text"), nil
}

func TestSafeEncoderMarshalerCalledOnce(t *testing.T) {
	var tomlCalls, textCalls int
	m := &safeCountMarshaler{calls: &tomlCalls}
	tm := &safeCountTextMarshaler{calls: &textCalls}

	// The same marshaler value shared by two branches must be called only
	// once; the result is reused.
	v := map[string]any{"a": m, "b": m, "t1": tm, "t2": tm}
	have, err := safeEncode(t, v)
	if err != nil {
		t.Fatal(err)
	}
	if tomlCalls != 1 {
		t.Errorf("MarshalTOML called %d times, want 1", tomlCalls)
	}
	if textCalls != 1 {
		t.Errorf("MarshalText called %d times, want 1", textCalls)
	}
	want := "a = \"marshalled\"\nb = \"marshalled\"\nt1 = \"text\"\nt2 = \"text\"\n"
	if have != want {
		t.Errorf("have: %q\nwant: %q", have, want)
	}
}

type safeErrMarshaler struct{ err error }

func (m safeErrMarshaler) MarshalTOML() ([]byte, error) { return nil, m.err }

func TestSafeEncoderMarshalerError(t *testing.T) {
	w := new(countWriter)
	err := NewSafeEncoder(w).Encode(map[string]any{"k": safeErrMarshaler{err: errors.New("boom")}})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want marshaler error, got: %v", err)
	}
	if w.calls != 0 {
		t.Errorf("writer was called %d times on marshaler error, want 0", w.calls)
	}
}

func TestSafeEncoderMaxBytes(t *testing.T) {
	v := map[string]any{"key": "value"}

	var buf bytes.Buffer
	if err := NewEncoder(&buf).Encode(v); err != nil {
		t.Fatal(err)
	}
	full := buf.String()

	// Exactly the output size is fine.
	w := new(countWriter)
	enc := NewSafeEncoder(w)
	enc.MaxBytes = len(full)
	if err := enc.Encode(v); err != nil {
		t.Fatalf("MaxBytes=%d should allow output of %d bytes: %v", len(full), len(full), err)
	}
	if w.buf.String() != full {
		t.Errorf("have: %q\nwant: %q", w.buf.String(), full)
	}

	// One byte less is not.
	w = new(countWriter)
	enc = NewSafeEncoder(w)
	enc.MaxBytes = len(full) - 1
	err := enc.Encode(v)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want byte limit error, got: %v", err)
	}
	if w.calls != 0 {
		t.Errorf("writer was called %d times on byte limit error, want 0", w.calls)
	}

	// Content returned by a custom marshaler counts towards the limit.
	var calls int
	big := &safeCountMarshaler{calls: &calls}
	w = new(countWriter)
	enc = NewSafeEncoder(w)
	enc.MaxBytes = 4
	err = enc.Encode(map[string]any{"k": big})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want byte limit error for marshaler content, got: %v", err)
	}
	if w.calls != 0 {
		t.Errorf("writer was called %d times on byte limit error, want 0", w.calls)
	}
}

func TestSafeEncoderWriterFailure(t *testing.T) {
	werr := errors.New("writer exploded")
	w := &countWriter{err: werr}
	err := NewSafeEncoder(w).Encode(map[string]any{"k": "v"})
	if !errors.Is(err, werr) {
		t.Fatalf("want writer error, got: %v", err)
	}
	if w.calls != 1 {
		t.Errorf("writer should be called exactly once on success path, got %d", w.calls)
	}
}

// The safe encoder must produce exactly the same output as the default
// Encoder for acyclic values: key order, datetimes, and number formatting.
func TestSafeEncoderMatchesEncoder(t *testing.T) {
	type sub struct {
		List []int `toml:"list"`
	}
	values := map[string]any{
		"ints":   []int{1, 2, 3},
		"float":  3.14,
		"float2": 1.0,
		"nan":    0.0 / 1.0, // plain zero; NaN can't be a constant
		"time":   time.Date(2021, 6, 7, 8, 9, 10, 123, time.UTC),
		"dur":    4 * time.Second,
		"sub":    sub{List: []int{4, 5}},
		"map":    map[string]any{"z": 1, "a": 2},
		"str":    "hello \"world\"",
		"bool":   true,
	}
	var want, have bytes.Buffer
	if err := NewEncoder(&want).Encode(values); err != nil {
		t.Fatal(err)
	}
	if err := NewSafeEncoder(&have).Encode(values); err != nil {
		t.Fatal(err)
	}
	if have.String() != want.String() {
		t.Errorf("output differs from Encoder:\nhave:\n%s\nwant:\n%s", have.String(), want.String())
	}
}
