package toml

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// shared vs. circular: a shared sub-object must encode fine, while a value
// that loops back to itself must report the path of the loop.
func TestSafeEncoderSharedVsCircular(t *testing.T) {
	type child struct {
		Name string
	}
	type parent struct {
		A *child
		B *child
	}

	shared := &child{Name: "shared"}
	out, err := MarshalSafe(parent{A: shared, B: shared})
	if err != nil {
		t.Fatalf("shared sub-object: unexpected error: %v", err)
	}
	want := "[A]\n  Name = \"shared\"\n\n[B]\n  Name = \"shared\"\n"
	if string(out) != want {
		t.Errorf("shared sub-object:\nhave:\n%s\nwant:\n%s", out, want)
	}

	type node struct {
		Name string
		Next *node
	}
	n := &node{Name: "loop"}
	n.Next = n

	_, err = MarshalSafe(n)
	if err == nil {
		t.Fatal("circular reference: expected an error, got none")
	}
	var circErr *CircularError
	if !errors.As(err, &circErr) {
		t.Fatalf("expected a *CircularError, got %T: %v", err, err)
	}
	if circErr.Path != "Next" {
		t.Errorf("error path: have %q, want %q", circErr.Path, "Next")
	}
	if !strings.Contains(err.Error(), "Next") {
		t.Errorf("error message should contain the path: %v", err)
	}
}

func TestSafeEncoderPointerCycle(t *testing.T) {
	type node struct {
		Next *node
	}
	a, b := &node{}, &node{}
	a.Next = b
	b.Next = a

	_, err := MarshalSafe(a)
	var circErr *CircularError
	if !errors.As(err, &circErr) {
		t.Fatalf("expected a *CircularError, got %T: %v", err, err)
	}
	if circErr.Path != "Next.Next" {
		t.Errorf("error path: have %q, want %q", circErr.Path, "Next.Next")
	}
}

func TestSafeEncoderMapSliceCycle(t *testing.T) {
	m := map[string]any{}
	s := []any{m}
	m["items"] = s

	_, err := MarshalSafe(m)
	var circErr *CircularError
	if !errors.As(err, &circErr) {
		t.Fatalf("expected a *CircularError, got %T: %v", err, err)
	}
	if circErr.Path != "items[0]" {
		t.Errorf("error path: have %q, want %q", circErr.Path, "items[0]")
	}

	// Slice containing a map containing the slice.
	s2 := make([]any, 1)
	m2 := map[string]any{"s": s2}
	s2[0] = m2

	_, err = MarshalSafe(s2)
	if !errors.As(err, &circErr) {
		t.Fatalf("expected a *CircularError, got %T: %v", err, err)
	}
	if circErr.Path != "[0].s" {
		t.Errorf("error path: have %q, want %q", circErr.Path, "[0].s")
	}
}

// Cycles hidden behind fields that are never encoded must be ignored.
func TestSafeEncoderIgnoredCycle(t *testing.T) {
	type node struct {
		Name    string
		Skipped *node `toml:"-"`
		hidden  *node
		Empty   *node `toml:",omitempty"`
	}
	n := &node{Name: "ok"}
	n.Skipped = n
	n.hidden = n

	out, err := MarshalSafe(n)
	if err != nil {
		t.Fatalf("ignored fields: unexpected error: %v", err)
	}
	if want := "Name = \"ok\"\n"; string(out) != want {
		t.Errorf("have:\n%s\nwant:\n%s", out, want)
	}
}

func TestSafeEncoderNil(t *testing.T) {
	type node struct {
		Name  string
		Ptr   *node
		Map   map[string]string
		Slice []string
		Any   any
	}
	out, err := MarshalSafe(node{Name: "nil"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "Name = \"nil\"\n"; string(out) != want {
		t.Errorf("have:\n%s\nwant:\n%s", out, want)
	}

	// Typed nil pointer at the top level.
	var p *node
	out, err = MarshalSafe(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty output, have %q", out)
	}
}

type countMarshaler struct {
	calls *int
}

func (c countMarshaler) MarshalTOML() ([]byte, error) {
	*c.calls++
	return []byte("42"), nil
}

// Custom marshalers must be called only once per value, even when the value
// is encoded more than once.
func TestSafeEncoderMarshalerCalledOnce(t *testing.T) {
	calls := 0
	shared := &countMarshaler{calls: &calls}
	v := map[string]any{"a": shared, "b": shared}

	out, err := MarshalSafe(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("MarshalTOML called %d times, want 1", calls)
	}
	if want := "a = 42\nb = 42\n"; string(out) != want {
		t.Errorf("have:\n%s\nwant:\n%s", out, want)
	}

	// The default Encoder keeps calling the marshaler for every occurrence.
	calls = 0
	if _, err := Marshal(v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("default Encoder: MarshalTOML called %d times, want 2", calls)
	}
}

type errMarshaler struct{ err error }

func (e errMarshaler) MarshalTOML() ([]byte, error) { return nil, e.err }

func TestSafeEncoderMarshalerError(t *testing.T) {
	w := &countingWriter{}
	err := NewSafeEncoder(w).Encode(map[string]any{"x": errMarshaler{err: errors.New("boom")}})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected the marshaler error, got: %v", err)
	}
	if w.calls != 0 {
		t.Errorf("writer called %d times, want 0", w.calls)
	}
}

func TestSafeEncoderMaxMarshalerBytes(t *testing.T) {
	calls := 0
	v := map[string]any{"x": &countMarshaler{calls: &calls}} // marshals to "42"

	w := &countingWriter{}
	enc := NewSafeEncoder(w)
	enc.MaxMarshalerBytes = 1
	err := enc.Encode(v)
	if err == nil || !strings.Contains(err.Error(), "exceeding the limit") {
		t.Fatalf("expected a byte limit error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "x") {
		t.Errorf("error should contain the key path: %v", err)
	}
	if w.calls != 0 {
		t.Errorf("writer called %d times, want 0", w.calls)
	}

	// A large enough limit passes.
	enc.MaxMarshalerBytes = 2
	if err := enc.Encode(v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

type countingWriter struct {
	calls int
	buf   strings.Builder
	err   error
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.err != nil {
		return 0, w.err
	}
	return w.buf.Write(p)
}

func TestSafeEncoderWriterFailure(t *testing.T) {
	w := &countingWriter{err: errors.New("disk full")}
	err := NewSafeEncoder(w).Encode(map[string]string{"a": "b"})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("expected the writer error, got: %v", err)
	}
}

// On any check failure the writer must not be called at all.
func TestSafeEncoderNoWriteOnFailure(t *testing.T) {
	type node struct {
		Next *node
	}
	n := &node{}
	n.Next = n

	w := &countingWriter{}
	if err := NewSafeEncoder(w).Encode(n); err == nil {
		t.Fatal("expected an error")
	}
	if w.calls != 0 {
		t.Errorf("writer called %d times, want 0", w.calls)
	}
}

// SafeEncoder output must be identical to Encoder output for values without
// cycles: same key order, datetime, and number representation.
func TestSafeEncoderMatchesEncoder(t *testing.T) {
	v := map[string]any{
		"z":     1,
		"a":     3.5,
		"when":  time.Date(2021, 6, 7, 8, 9, 10, 0, time.UTC),
		"table": map[string]any{"b": true, "a": "x"},
		"list":  []int{1, 2, 3},
	}
	safe, err := MarshalSafe(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	plain, err := Marshal(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(safe) != string(plain) {
		t.Errorf("output differs:\nsafe:\n%s\nplain:\n%s", safe, plain)
	}
}

func ExampleSafeEncoder() {
	type Server struct {
		Host string
		Port int
	}
	out, err := MarshalSafe(Server{Host: "localhost", Port: 8080})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Print(string(out))

	// Output:
	// Host = "localhost"
	// Port = 8080
}

func ExampleSafeEncoder_circular() {
	type Node struct {
		Name string
		Next *Node `toml:"next"`
	}
	n := &Node{Name: "a"}
	n.Next = n

	_, err := MarshalSafe(n)
	fmt.Println(err)

	// Output:
	// toml: circular reference at key "next"
}