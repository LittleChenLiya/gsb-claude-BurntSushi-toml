package toml_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// recordReader records every Read request: how many reads were made, the
// largest requested buffer size, and the total number of bytes read.
type recordReader struct {
	data   []byte
	chunk  int // maximum bytes returned per Read; 0 = fill p
	reads  int
	maxReq int
	total  int
	err    error // returned instead of io.EOF once data is exhausted
}

func (r *recordReader) Read(p []byte) (int, error) {
	r.reads++
	if len(p) > r.maxReq {
		r.maxReq = len(p)
	}
	if r.total >= len(r.data) {
		if r.err != nil {
			return 0, r.err
		}
		return 0, io.EOF
	}
	n := copy(p, r.data[r.total:])
	if r.chunk > 0 && n > r.chunk {
		n = r.chunk
	}
	r.total += n
	return n, nil
}

type cancelReader struct {
	cancel context.CancelFunc
	reads  int
}

func (r *cancelReader) Read(p []byte) (int, error) {
	r.reads++
	r.cancel()
	return 0, nil
}

func asResourceLimitError(t *testing.T, err error) toml.ResourceLimitError {
	t.Helper()
	var rlErr toml.ResourceLimitError
	if !errors.As(err, &rlErr) {
		t.Fatalf("expected a ResourceLimitError, got %T: %v", err, err)
	}
	return rlErr
}

func mustSetLimits(t *testing.T, dec *toml.Decoder, l toml.ResourceLimits) {
	t.Helper()
	if err := dec.SetResourceLimits(l); err != nil {
		t.Fatalf("SetResourceLimits: %v", err)
	}
}

func TestResourceLimitInputBytes(t *testing.T) {
	doc := "abc = 1" + "\n" + "def = 2" + "\n"

	// Exactly at the limit: still valid.
	dec := toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxInputBytes: int64(len(doc))})
	var v map[string]int64
	if _, err := dec.Decode(&v); err != nil {
		t.Fatalf("exactly-at-limit decode failed: %v", err)
	}
	if v["abc"] != 1 || v["def"] != 2 {
		t.Fatalf("unexpected decode result: %v", v)
	}

	// Over the limit: the first byte beyond the limit is the "d" of "def"
	// on line 2, column 1.
	dec = toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxInputBytes: int64(len(doc) - 8)})
	_, err := dec.Decode(&v)
	rlErr := asResourceLimitError(t, err)
	if rlErr.Name != "MaxInputBytes" {
		t.Errorf("Name = %q, want MaxInputBytes", rlErr.Name)
	}
	if rlErr.Limit != int64(len(doc)-8) {
		t.Errorf("Limit = %d, want %d", rlErr.Limit, len(doc)-8)
	}
	if rlErr.Line != 2 || rlErr.Col != 1 {
		t.Errorf("position = %d:%d, want 2:1", rlErr.Line, rlErr.Col)
	}
}

func TestResourceLimitInputBytesReaderStops(t *testing.T) {
	// 100kB of input with a limit of 100 bytes: reading must stop as soon
	// as the limit is exceeded instead of reading the whole input.
	doc := strings.Repeat("abcde = 1\n", 10000)
	rr := &recordReader{data: []byte(doc)}
	dec := toml.NewDecoder(rr)
	mustSetLimits(t, dec, toml.ResourceLimits{MaxInputBytes: 100})

	var v map[string]int64
	_, err := dec.Decode(&v)
	asResourceLimitError(t, err)

	if rr.total != 101 {
		t.Errorf("read %d bytes, want exactly limit+1 = 101", rr.total)
	}
	if rr.maxReq > 32*1024 {
		t.Errorf("Read request of %d bytes exceeds the 32kB bound", rr.maxReq)
	}
	reads := rr.reads
	if reads == 0 {
		t.Fatal("expected at least one Read")
	}
	// Decode returned: no further Read calls may happen.
	if rr.reads != reads {
		t.Fatalf("extra Read calls after error: %d -> %d", reads, rr.reads)
	}
}

func TestResourceLimitReadRequestBound(t *testing.T) {
	// Even without any limits, a single Read request must stay bounded.
	var sb strings.Builder
	for i := 0; i < 10000; i++ {
		fmt.Fprintf(&sb, "k%d = 1\n", i)
	}
	doc := sb.String()
	rr := &recordReader{data: []byte(doc)}
	var v map[string]int64
	if _, err := toml.NewDecoder(rr).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if rr.maxReq > 32*1024 {
		t.Errorf("Read request of %d bytes exceeds the 32kB bound", rr.maxReq)
	}
	if rr.total != len(doc) {
		t.Errorf("read %d bytes, want %d", rr.total, len(doc))
	}
}

func TestResourceLimitKeyAssignments(t *testing.T) {
	// Table headers ([t]) do not count as assignments: only a = 1 and
	// b = 2 do.
	doc := "[t]\na = 1\nb = 2\n"
	dec := toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxKeyAssignments: 2})
	var v map[string]map[string]int64
	if _, err := dec.Decode(&v); err != nil {
		t.Fatalf("decode with exactly 2 assignments failed: %v", err)
	}

	dec = toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxKeyAssignments: 1})
	_, err := dec.Decode(&v)
	rlErr := asResourceLimitError(t, err)
	if rlErr.Name != "MaxKeyAssignments" || rlErr.Limit != 1 {
		t.Errorf("got %v, want MaxKeyAssignments limit 1", rlErr)
	}
	if rlErr.Line != 3 {
		t.Errorf("Line = %d, want 3", rlErr.Line)
	}

	// Assignments inside inline tables count as well.
	doc = "x = {c = 1, d = 2}\n"
	dec = toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxKeyAssignments: 3})
	var w map[string]any
	if _, err := dec.Decode(&w); err != nil {
		t.Fatalf("decode with exactly 3 assignments failed: %v", err)
	}
	dec = toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxKeyAssignments: 2})
	if _, err := dec.Decode(&w); err == nil {
		t.Fatal("expected MaxKeyAssignments error for inline table")
	} else {
		rlErr := asResourceLimitError(t, err)
		if rlErr.Name != "MaxKeyAssignments" {
			t.Errorf("Name = %q, want MaxKeyAssignments", rlErr.Name)
		}
	}
}

type prefillTarget struct {
	Name string
	Ptr  *int
	Map  map[string]int
	List []string
}

func TestResourceLimitTargetUnchanged(t *testing.T) {
	// A document that would overwrite every field, but exceeds the limit
	// before decoding finishes.
	doc := "name = \"changed\"\n" +
		"list = [\"a\", \"b\"]\n" +
		"[map]\n" +
		"a = 2\n"

	newTarget := func() *prefillTarget {
		v := 42
		return &prefillTarget{
			Name: "orig",
			Ptr:  &v,
			Map:  map[string]int{"k": 9},
			List: []string{"orig"},
		}
	}

	for _, limits := range []toml.ResourceLimits{
		{MaxKeyAssignments: 1},
		{MaxInputBytes: 5},
		{MaxStringBytes: 3},
		{MaxArrayElements: 1},
	} {
		tgt := newTarget()
		origPtr := tgt.Ptr
		dec := toml.NewDecoder(strings.NewReader(doc))
		mustSetLimits(t, dec, limits)
		if _, err := dec.Decode(tgt); err == nil {
			t.Fatalf("expected limit error for %+v", limits)
		} else {
			asResourceLimitError(t, err)
		}

		if tgt.Name != "orig" {
			t.Errorf("%+v: Name = %q, want %q", limits, tgt.Name, "orig")
		}
		if tgt.Ptr != origPtr || *tgt.Ptr != 42 {
			t.Errorf("%+v: Ptr changed: %v -> %v", limits, origPtr, tgt.Ptr)
		}
		if !reflect.DeepEqual(tgt.Map, map[string]int{"k": 9}) {
			t.Errorf("%+v: Map = %v, want map[k:9]", limits, tgt.Map)
		}
		if !reflect.DeepEqual(tgt.List, []string{"orig"}) {
			t.Errorf("%+v: List = %v, want [orig]", limits, tgt.List)
		}
	}
}

func TestResourceLimitNegative(t *testing.T) {
	for _, l := range []toml.ResourceLimits{
		{MaxInputBytes: -1},
		{MaxKeyAssignments: -1},
		{MaxStringBytes: -1},
		{MaxArrayElements: -1},
	} {
		if err := toml.NewDecoder(strings.NewReader("")).SetResourceLimits(l); err == nil {
			t.Errorf("expected error for negative limit %+v", l)
		}
	}
	// Zero (disabled) is fine.
	if err := toml.NewDecoder(strings.NewReader("")).SetResourceLimits(toml.ResourceLimits{}); err != nil {
		t.Errorf("unexpected error for zero limits: %v", err)
	}
}

func TestResourceLimitMaxDepthOrder(t *testing.T) {
	// The table header on line 1 exceeds MaxDepth before any assignment
	// limit can trigger: the depth error is reported.
	dec := toml.NewDecoder(strings.NewReader("[a]\nk = 1\n"))
	dec.MaxDepth(1)
	mustSetLimits(t, dec, toml.ResourceLimits{MaxKeyAssignments: 1})
	var v map[string]any
	_, err := dec.Decode(&v)
	if err == nil {
		t.Fatal("expected MaxDepth error")
	}
	var rlErr toml.ResourceLimitError
	if errors.As(err, &rlErr) {
		t.Fatalf("expected a MaxDepth error, got resource limit error: %v", rlErr)
	}
	if !strings.Contains(err.Error(), "maximum table depth") {
		t.Fatalf("expected MaxDepth error, got: %v", err)
	}

	// The second assignment on line 2 exceeds MaxKeyAssignments before the
	// table on line 3 exceeds MaxDepth: the resource limit is reported.
	dec = toml.NewDecoder(strings.NewReader("k1 = 1\nk2 = 2\n[a]\n"))
	dec.MaxDepth(1)
	mustSetLimits(t, dec, toml.ResourceLimits{MaxKeyAssignments: 1})
	_, err = dec.Decode(&v)
	rlErr = asResourceLimitError(t, err)
	if rlErr.Name != "MaxKeyAssignments" {
		t.Errorf("Name = %q, want MaxKeyAssignments", rlErr.Name)
	}
	if rlErr.Line != 2 {
		t.Errorf("Line = %d, want 2", rlErr.Line)
	}
}

func TestResourceLimitContextCancel(t *testing.T) {
	doc := strings.Repeat("abcde = 1\n", 1000)

	// Cancelled before Decode: no Read calls at all.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rr := &recordReader{data: []byte(doc)}
	dec := toml.NewDecoder(rr)
	dec.SetContext(ctx)
	var v map[string]int64
	_, err := dec.Decode(&v)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if rr.reads != 0 {
		t.Errorf("%d Read calls after cancellation, want 0", rr.reads)
	}

	// Cancelled during the first Read: no further Read calls.
	ctx2, cancel2 := context.WithCancel(context.Background())
	cr := &cancelReader{cancel: cancel2}
	dec = toml.NewDecoder(cr)
	dec.SetContext(ctx2)
	_, err = dec.Decode(&v)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if cr.reads != 1 {
		t.Errorf("%d Read calls, want exactly 1 (no reads after cancel)", cr.reads)
	}
}

func TestResourceLimitReaderErrorPreserved(t *testing.T) {
	sentinel := errors.New("sentinel read error")
	rr := &recordReader{data: []byte("a = 1\n"), err: sentinel}
	var v map[string]int64
	_, err := toml.NewDecoder(rr).Decode(&v)
	if !errors.Is(err, sentinel) {
		t.Fatalf("reader error not identifiable: %v", err)
	}
}

func TestResourceLimitMultibyteAcrossReads(t *testing.T) {
	// Multi-byte UTF-8 characters split across many small Read calls.
	doc := "s = \"héllo wörld\"\n"
	rr := &recordReader{data: []byte(doc), chunk: 1}
	dec := toml.NewDecoder(rr)
	mustSetLimits(t, dec, toml.ResourceLimits{
		MaxInputBytes:  int64(len(doc)),
		MaxStringBytes: 13, // "héllo wörld" is 13 UTF-8 bytes
	})
	var v map[string]string
	if _, err := dec.Decode(&v); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if v["s"] != "héllo wörld" {
		t.Fatalf("s = %q, want %q", v["s"], "héllo wörld")
	}

	// One byte less than the document size exceeds MaxInputBytes.
	rr = &recordReader{data: []byte(doc), chunk: 1}
	dec = toml.NewDecoder(rr)
	mustSetLimits(t, dec, toml.ResourceLimits{MaxInputBytes: int64(len(doc) - 1)})
	if _, err := dec.Decode(&v); err == nil {
		t.Fatal("expected MaxInputBytes error")
	} else {
		asResourceLimitError(t, err)
	}
}

func TestResourceLimitSuccessMetaData(t *testing.T) {
	// Normal decoding and MetaData behaviour are unchanged when limits are
	// enabled and not exceeded.
	doc := "a = 1\n[b]\nc = \"x\"\n"
	dec := toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{
		MaxInputBytes:     1024,
		MaxKeyAssignments: 10,
		MaxStringBytes:    64,
		MaxArrayElements:  16,
	})
	var v struct {
		A int
		B struct{ C string }
	}
	md, err := dec.Decode(&v)
	if err != nil {
		t.Fatal(err)
	}
	if v.A != 1 || v.B.C != "x" {
		t.Fatalf("unexpected decode result: %+v", v)
	}
	if !md.IsDefined("a") || !md.IsDefined("b", "c") {
		t.Errorf("IsDefined: keys not recorded: %v", md.Keys())
	}
	if md.IsDefined("nope") {
		t.Error("IsDefined: undefined key reported as defined")
	}
	if u := md.Undecoded(); len(u) != 0 {
		t.Errorf("Undecoded = %v, want empty", u)
	}
}

func ExampleDecoder_SetResourceLimits() {
	var v struct {
		Name string
		Tags []string
	}
	dec := toml.NewDecoder(strings.NewReader("name = \"toml\"\ntags = [\"a\", \"b\"]\n"))
	err := dec.SetResourceLimits(toml.ResourceLimits{
		MaxInputBytes:     1024,
		MaxKeyAssignments: 10,
		MaxStringBytes:    64,
		MaxArrayElements:  16,
	})
	if err != nil {
		panic(err)
	}
	if _, err := dec.Decode(&v); err != nil {
		panic(err)
	}
	fmt.Println(v.Name, v.Tags)
	// Output: toml [a b]
}

func ExampleDecoder_SetResourceLimits_exceeded() {
	var v map[string]any
	dec := toml.NewDecoder(strings.NewReader("a = 1\nb = 2\n"))
	if err := dec.SetResourceLimits(toml.ResourceLimits{MaxKeyAssignments: 1}); err != nil {
		panic(err)
	}
	_, err := dec.Decode(&v)
	var rlErr toml.ResourceLimitError
	if errors.As(err, &rlErr) {
		fmt.Println(rlErr)
	}
	// Output: toml: resource limit MaxKeyAssignments of 1 exceeded at line 2, column 5
}

func TestResourceLimitStringBytes(t *testing.T) {
	// "a\u00e9b" decodes to "aéb": 4 UTF-8 bytes after escape expansion.
	doc := "s = \"a\u00e9b\""

	dec := toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxStringBytes: 4})
	var v map[string]string
	if _, err := dec.Decode(&v); err != nil {
		t.Fatalf("decode with exactly 4 string bytes failed: %v", err)
	}
	if v["s"] != "aéb" {
		t.Fatalf("s = %q, want %q", v["s"], "aéb")
	}

	dec = toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxStringBytes: 3})
	_, err := dec.Decode(&v)
	rlErr := asResourceLimitError(t, err)
	if rlErr.Name != "MaxStringBytes" || rlErr.Limit != 3 {
		t.Errorf("got %v, want MaxStringBytes limit 3", rlErr)
	}
	if rlErr.Line != 1 {
		t.Errorf("Line = %d, want 1", rlErr.Line)
	}
}

func TestResourceLimitStringBytesMultiline(t *testing.T) {
	// The first newline and the whitespace after a line-ending backslash
	// are removed per the TOML spec before counting: the decoded string is
	// "abcdef" (6 bytes).
	doc := "s = \"\"\"" + "\n" + "abc\\" + "\n" + "   def\"\"\""

	dec := toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxStringBytes: 6})
	var v map[string]string
	if _, err := dec.Decode(&v); err != nil {
		t.Fatalf("decode with exactly 6 string bytes failed: %v", err)
	}
	if v["s"] != "abcdef" {
		t.Fatalf("s = %q, want %q", v["s"], "abcdef")
	}

	dec = toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxStringBytes: 5})
	_, err := dec.Decode(&v)
	rlErr := asResourceLimitError(t, err)
	if rlErr.Name != "MaxStringBytes" || rlErr.Limit != 5 {
		t.Errorf("got %v, want MaxStringBytes limit 5", rlErr)
	}
}

func TestResourceLimitStringBytesRawMultiline(t *testing.T) {
	// Raw multiline string: only the first newline is stripped. 'héllo' is
	// 6 UTF-8 bytes.
	doc := "s = '''" + "\n" + "héllo'''"

	dec := toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxStringBytes: 6})
	var v map[string]string
	if _, err := dec.Decode(&v); err != nil {
		t.Fatalf("decode with exactly 6 string bytes failed: %v", err)
	}

	dec = toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxStringBytes: 5})
	_, err := dec.Decode(&v)
	rlErr := asResourceLimitError(t, err)
	if rlErr.Name != "MaxStringBytes" || rlErr.Limit != 5 {
		t.Errorf("got %v, want MaxStringBytes limit 5", rlErr)
	}
	if rlErr.Line != 2 {
		t.Errorf("Line = %d, want 2", rlErr.Line)
	}
}

func TestResourceLimitArrayElements(t *testing.T) {
	// arr has 3 top-level elements; the nested array [2, 3] counts as one
	// element of arr and contributes 2 more elements; the inline table
	// counts as one element. Each [[t]] entry counts as one element.
	// Total: 5 + 2 = 7 elements.
	doc := "arr = [1, [2, 3], {a = 1}]\n" +
		"[[t]]\n" +
		"x = 1\n" +
		"[[t]]\n" +
		"x = 2\n"

	dec := toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxArrayElements: 7})
	var v map[string]any
	if _, err := dec.Decode(&v); err != nil {
		t.Fatalf("decode with exactly 7 array elements failed: %v", err)
	}

	dec = toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxArrayElements: 6})
	_, err := dec.Decode(&v)
	rlErr := asResourceLimitError(t, err)
	if rlErr.Name != "MaxArrayElements" || rlErr.Limit != 6 {
		t.Errorf("got %v, want MaxArrayElements limit 6", rlErr)
	}
	if rlErr.Line != 4 || rlErr.Col != 1 {
		t.Errorf("position = %d:%d, want 4:1", rlErr.Line, rlErr.Col)
	}

	// Nested containers count as a single element of the parent array:
	// [[1, 2], [3]] has 2 top-level and 3 nested elements: 5 in total.
	doc = "arr = [[1, 2], [3]]\n"
	dec = toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxArrayElements: 5})
	if _, err := dec.Decode(&v); err != nil {
		t.Fatalf("decode with exactly 5 array elements failed: %v", err)
	}
	dec = toml.NewDecoder(strings.NewReader(doc))
	mustSetLimits(t, dec, toml.ResourceLimits{MaxArrayElements: 4})
	if _, err := dec.Decode(&v); err == nil {
		t.Fatal("expected MaxArrayElements error")
	}
}
