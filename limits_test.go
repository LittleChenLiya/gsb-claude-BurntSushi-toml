package toml

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

// chunkReader returns at most n bytes per Read call; it proves multibyte
// runes and limits work across arbitrary Read boundaries.
type chunkReader struct {
	data []byte
	n    int
	pos  int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := r.n
	if n > len(p) {
		n = len(p)
	}
	if n > len(r.data)-r.pos {
		n = len(r.data) - r.pos
	}
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

// recordingReader records the number of Read calls, the largest buffer ever
// requested, and the total number of bytes read.
type recordingReader struct {
	r         io.Reader
	calls     int
	maxReq    int
	bytesRead int
	onRead    func()
}

func (r *recordingReader) Read(p []byte) (int, error) {
	r.calls++
	if len(p) > r.maxReq {
		r.maxReq = len(p)
	}
	if r.onRead != nil {
		r.onRead()
	}
	n, err := r.r.Read(p)
	r.bytesRead += n
	return n, err
}

// keyGenReader emits an endless stream of unique "kN = 1" assignments
// without ever allocating a large input.
type keyGenReader struct {
	n   int
	buf []byte
}

func (r *keyGenReader) Read(p []byte) (int, error) {
	total := 0
	for total < len(p) {
		if len(r.buf) == 0 {
			r.n++
			r.buf = []byte(fmt.Sprintf("k%d = 1\n", r.n))
		}
		c := copy(p[total:], r.buf)
		r.buf = r.buf[c:]
		total += c
	}
	return total, nil
}

type boomReader struct{ pos int }

var errBoom = errors.New("boom from reader")

func (r *boomReader) Read(p []byte) (int, error) {
	data := []byte("x = 1\n")
	if r.pos >= len(data) {
		return 0, errBoom
	}
	n := 2
	if r.pos+n > len(data) {
		n = len(data) - r.pos
	}
	copy(p, data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

func parseLimitError(t *testing.T, err error) (ParseError, LimitError) {
	t.Helper()
	var pe ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("want toml.ParseError, got %T: %v", err, err)
	}
	var le LimitError
	if !errors.As(err, &le) {
		t.Fatalf("want toml.LimitError wrapped in ParseError, got: %v", err)
	}
	return pe, le
}

func TestLimitsString(t *testing.T) {
	cases := []struct {
		name        string
		doc         string
		limit       int
		wantOK      bool
		wantLine    int
		wantCol     int // -1: don't check
		wantDecoded string
	}{{
		name:   "escape exact",
		doc:    `s = "A\tB"`,
		limit:  3,
		wantOK: true,
	}, {
		name:     "escape exceed",
		doc:      `s = "A\tB"`,
		limit:    2,
		wantLine: 1,
		wantCol:  9,
	}, {
		name:   "unicode counts decoded bytes, not raw",
		doc:    `s = "\u0041\u0042"`,
		limit:  2,
		wantOK: true,
	}, {
		name:     "unicode exceed",
		doc:      `s = "\u0041\u0042"`,
		limit:    1,
		wantLine: 1,
		wantCol:  -1,
	}, {
		name:   "multiline strips first newline exact",
		doc:    "s = " + `"""` + "\nabc" + `"""`,
		limit:  3,
		wantOK: true,
	}, {
		name:     "multiline strips first newline exceed",
		doc:      "s = " + `"""` + "\nabc" + `"""`,
		limit:    2,
		wantLine: 2,
		wantCol:  3,
	}, {
		name:   "multiline line-ending backslash exact",
		doc:    "s = " + `"""` + "a\\\n    b" + `"""`,
		limit:  2,
		wantOK: true,
	}, {
		name:     "multiline line-ending backslash exceed",
		doc:      "s = " + `"""` + "a\\\n    b" + `"""`,
		limit:    1,
		wantLine: 2,
		wantCol:  5,
	}, {
		name:        "multibyte across 1-byte reads exact",
		doc:         `s = "héllo 🚀 end"`,
		limit:       len("héllo 🚀 end"),
		wantOK:      true,
		wantDecoded: "héllo 🚀 end",
	}, {
		name:     "multibyte across 1-byte reads exceed",
		doc:      `s = "héllo 🚀 end"`,
		limit:    len("héllo 🚀 end") - 1,
		wantLine: 1,
		wantCol:  -1,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var v struct{ S string }
			dec := NewDecoder(&chunkReader{data: []byte(tc.doc), n: 1})
			if err := dec.MaxStringBytes(tc.limit); err != nil {
				t.Fatal(err)
			}
			_, err := dec.Decode(&v)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.wantDecoded != "" && v.S != tc.wantDecoded {
					t.Errorf("decoded %q, want %q", v.S, tc.wantDecoded)
				}
				return
			}
			if err == nil {
				t.Fatalf("want MaxStringBytes error, got nil; decoded %q", v.S)
			}
			pe, le := parseLimitError(t, err)
			if le.Name != "MaxStringBytes" || int(le.Limit) != tc.limit {
				t.Errorf("limit error = %s (%d), want MaxStringBytes %d", le.Name, le.Limit, tc.limit)
			}
			if pe.Position.Line != tc.wantLine {
				t.Errorf("line = %d, want %d; %s", pe.Position.Line, tc.wantLine, pe.ErrorWithPosition())
			}
			if tc.wantCol >= 0 && pe.Position.Col != tc.wantCol {
				t.Errorf("col = %d, want %d; %s", pe.Position.Col, tc.wantCol, pe.ErrorWithPosition())
			}
		})
	}
}

func TestLimitsKeyAssignments(t *testing.T) {
	t.Run("headers do not count exact", func(t *testing.T) {
		var v map[string]any
		dec := NewDecoder(strings.NewReader("[a]\n[b]\nk = 1\n"))
		if err := dec.MaxKeyAssignments(1); err != nil {
			t.Fatal(err)
		}
		if _, err := dec.Decode(&v); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("table keys exact and exceed", func(t *testing.T) {
		var v map[string]any
		dec := NewDecoder(strings.NewReader("[t]\nk1 = 1\nk2 = 2\n"))
		if err := dec.MaxKeyAssignments(2); err != nil {
			t.Fatal(err)
		}
		if _, err := dec.Decode(&v); err != nil {
			t.Fatalf("unexpected error at limit: %v", err)
		}

		dec = NewDecoder(strings.NewReader("[t]\nk1 = 1\nk2 = 2\n"))
		if err := dec.MaxKeyAssignments(1); err != nil {
			t.Fatal(err)
		}
		_, err := dec.Decode(&v)
		pe, le := parseLimitError(t, err)
		if le.Name != "MaxKeyAssignments" || le.Limit != 1 {
			t.Errorf("unexpected limit error: %v", le)
		}
		if pe.Position.Line != 3 {
			t.Errorf("line = %d, want 3", pe.Position.Line)
		}
	})

	t.Run("inline table assignments exact and exceed", func(t *testing.T) {
		var v map[string]any
		dec := NewDecoder(strings.NewReader("t = {a = 1, b = 2}\n"))
		if err := dec.MaxKeyAssignments(3); err != nil {
			t.Fatal(err)
		}
		if _, err := dec.Decode(&v); err != nil {
			t.Fatalf("unexpected error at limit: %v", err)
		}

		dec = NewDecoder(strings.NewReader("t = {a = 1, b = 2}\n"))
		if err := dec.MaxKeyAssignments(2); err != nil {
			t.Fatal(err)
		}
		_, err := dec.Decode(&v)
		pe, le := parseLimitError(t, err)
		if le.Name != "MaxKeyAssignments" || pe.Position.Line != 1 {
			t.Errorf("unexpected error: %v at line %d", le, pe.Position.Line)
		}
	})
}

func TestLimitsArrayElements(t *testing.T) {
	t.Run("nested arrays exact and exceed", func(t *testing.T) {
		// Outer array has 2 elements; the inner arrays add 2 and 1: 5 in all.
		doc := "a = [[1, 2], [3]]\n"
		var v map[string]any
		dec := NewDecoder(strings.NewReader(doc))
		if err := dec.MaxArrayElements(5); err != nil {
			t.Fatal(err)
		}
		if _, err := dec.Decode(&v); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		dec = NewDecoder(&chunkReader{data: []byte(doc), n: 2})
		if err := dec.MaxArrayElements(4); err != nil {
			t.Fatal(err)
		}
		_, err := dec.Decode(&v)
		pe, le := parseLimitError(t, err)
		if le.Name != "MaxArrayElements" || le.Limit != 4 || pe.Position.Line != 1 {
			t.Errorf("unexpected error: %v", le)
		}
	})

	t.Run("array of tables exact and exceed", func(t *testing.T) {
		doc := "[[t]]\na = 1\n[[t]]\nb = 2\n"
		var v map[string]any
		dec := NewDecoder(strings.NewReader(doc))
		if err := dec.MaxArrayElements(2); err != nil {
			t.Fatal(err)
		}
		if _, err := dec.Decode(&v); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		dec = NewDecoder(strings.NewReader(doc))
		if err := dec.MaxArrayElements(1); err != nil {
			t.Fatal(err)
		}
		_, err := dec.Decode(&v)
		pe, le := parseLimitError(t, err)
		if le.Name != "MaxArrayElements" || le.Limit != 1 || pe.Position.Line != 3 {
			t.Errorf("unexpected error: %v at line %d", le, pe.Position.Line)
		}
	})

	t.Run("nested container is one parent element", func(t *testing.T) {
		// The inline table is one element; its own keys count separately.
		var v map[string]any
		dec := NewDecoder(strings.NewReader("a = [{x = 1, y = 2}]\n"))
		if err := dec.MaxArrayElements(1); err != nil {
			t.Fatal(err)
		}
		if _, err := dec.Decode(&v); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestLimitsInputBytes(t *testing.T) {
	doc := "a = 1\nb = 2\n"

	t.Run("exact", func(t *testing.T) {
		var v map[string]any
		dec := NewDecoder(&chunkReader{data: []byte(doc), n: 3})
		if err := dec.MaxInputBytes(int64(len(doc))); err != nil {
			t.Fatal(err)
		}
		if _, err := dec.Decode(&v); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("exceed", func(t *testing.T) {
		var v map[string]any
		dec := NewDecoder(&chunkReader{data: []byte(doc), n: 3})
		// Byte offset 6 is the 'b' of the second key: line 2, column 1.
		if err := dec.MaxInputBytes(6); err != nil {
			t.Fatal(err)
		}
		_, err := dec.Decode(&v)
		pe, le := parseLimitError(t, err)
		if le.Name != "MaxInputBytes" || le.Limit != 6 {
			t.Errorf("unexpected limit error: %v", le)
		}
		if pe.Position.Line != 2 || pe.Position.Col != 1 {
			t.Errorf("position = %d:%d, want 2:1", pe.Position.Line, pe.Position.Col)
		}
	})
}

func TestLimitsTargetUnchanged(t *testing.T) {
	type target struct {
		S     string
		P     *int
		M     map[string]int
		Slice []string
	}
	n := 42
	newTarget := func() *target {
		return &target{
			S:     "original",
			P:     &n,
			M:     map[string]int{"keep": 1},
			Slice: []string{"keep"},
		}
	}

	// Every document exceeds one of the limits after the first key.
	docs := []struct {
		name  string
		doc   string
		setup func(*Decoder) error
	}{
		{"input bytes", "S = \"new\"\nM = {other = 2}\n", func(d *Decoder) error {
			return d.MaxInputBytes(5)
		}},
		{"key assignments", "S = \"new\"\nM = {other = 2}\n", func(d *Decoder) error {
			return d.MaxKeyAssignments(1)
		}},
		{"string bytes", "S = \"a much longer string than allowed\"\n", func(d *Decoder) error {
			return d.MaxStringBytes(3)
		}},
		{"array elements", "Slice = [\"a\", \"b\", \"c\"]\n", func(d *Decoder) error {
			return d.MaxArrayElements(1)
		}},
	}

	for _, tc := range docs {
		t.Run(tc.name, func(t *testing.T) {
			v := newTarget()
			want := *v
			dec := NewDecoder(strings.NewReader(tc.doc))
			if err := tc.setup(dec); err != nil {
				t.Fatal(err)
			}
			_, err := dec.Decode(v)
			if err == nil {
				t.Fatal("want limit error, got nil")
			}
			var le LimitError
			if !errors.As(err, &le) {
				t.Fatalf("want LimitError, got: %v", err)
			}
			if v.S != want.S {
				t.Errorf("S = %q, want unchanged %q", v.S, want.S)
			}
			if v.P != want.P || *v.P != *want.P {
				t.Errorf("P changed: %v -> %v", *want.P, *v.P)
			}
			if !reflect.DeepEqual(v.M, want.M) {
				t.Errorf("M = %v, want unchanged %v", v.M, want.M)
			}
			if !reflect.DeepEqual(v.Slice, want.Slice) {
				t.Errorf("Slice = %v, want unchanged %v", v.Slice, want.Slice)
			}
		})
	}
}

func TestLimitsStopsReading(t *testing.T) {
	// A document far larger than the limit; the reader must not be drained.
	var sb strings.Builder
	sb.WriteString("k = \"")
	for i := 0; i < 5000; i++ {
		sb.WriteString("abcdefgh")
	}
	sb.WriteString("\"\n")
	doc := sb.String()

	rec := &recordingReader{r: strings.NewReader(doc)}
	dec := NewDecoder(rec)
	if err := dec.MaxStringBytes(10); err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	_, err := dec.Decode(&v)
	var le LimitError
	if !errors.As(err, &le) {
		t.Fatalf("want LimitError, got %v", err)
	}
	if rec.bytesRead > 2*maxReadChunk {
		t.Errorf("read %d bytes of a %d-byte document; reading did not stop at the limit",
			rec.bytesRead, len(doc))
	}
	calls := rec.calls

	// No further reads may happen after the error was returned.
	if rec.calls != calls {
		t.Errorf("extra Read calls after Decode returned")
	}
}

func TestLimitsReadChunkBound(t *testing.T) {
	rec := &recordingReader{r: strings.NewReader("k = \"v\"\n")}
	var v map[string]any
	if _, err := NewDecoder(rec).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if rec.maxReq > maxReadChunk {
		t.Errorf("single Read requested %d bytes; bound is %d", rec.maxReq, maxReadChunk)
	}
}

func TestLimitsGeneratedInput(t *testing.T) {
	// An endless document proves limits stop the decoder without allocating
	// the input, and that no reads happen after cancellation.
	t.Run("key assignments stop infinite input", func(t *testing.T) {
		rec := &recordingReader{r: &keyGenReader{}}
		dec := NewDecoder(rec)
		if err := dec.MaxKeyAssignments(3); err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		_, err := dec.Decode(&v)
		_, le := parseLimitError(t, err)
		if le.Name != "MaxKeyAssignments" {
			t.Errorf("unexpected limit error: %v", le)
		}
		if rec.bytesRead > 2*maxReadChunk {
			t.Errorf("read %d bytes; expected to stop shortly after the limit", rec.bytesRead)
		}
	})

	t.Run("no reads after cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		readsAfterCancel := 0
		cancelled := false
		rec := &recordingReader{
			r: &keyGenReader{},
		}
		rec.onRead = func() {
			if cancelled {
				readsAfterCancel++
			}
			if rec.calls == 2 {
				cancelled = true
				cancel()
			}
		}
		var v map[string]any
		_, err := NewDecoder(rec).DecodeContext(ctx, &v)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
		if readsAfterCancel != 0 {
			t.Errorf("%d Read calls after cancellation", readsAfterCancel)
		}
	})
}

func TestLimitsReaderError(t *testing.T) {
	var v map[string]any
	_, err := NewDecoder(&boomReader{}).Decode(&v)
	if !errors.Is(err, errBoom) {
		t.Fatalf("want errBoom, got %v", err)
	}
	var pe ParseError
	if errors.As(err, &pe) {
		t.Errorf("reader error must not be reported as a ParseError: %v", err)
	}
}

func TestLimitsNegative(t *testing.T) {
	dec := NewDecoder(strings.NewReader(""))
	if err := dec.MaxInputBytes(-1); err == nil {
		t.Error("MaxInputBytes(-1): want error")
	}
	if err := dec.MaxKeyAssignments(-1); err == nil {
		t.Error("MaxKeyAssignments(-1): want error")
	}
	if err := dec.MaxStringBytes(-1); err == nil {
		t.Error("MaxStringBytes(-1): want error")
	}
	if err := dec.MaxArrayElements(-1); err == nil {
		t.Error("MaxArrayElements(-1): want error")
	}

	// Zero disables the limit and is fine.
	for _, err := range []error{
		dec.MaxInputBytes(0),
		dec.MaxKeyAssignments(0),
		dec.MaxStringBytes(0),
		dec.MaxArrayElements(0),
	} {
		if err != nil {
			t.Errorf("zero limit should be accepted: %v", err)
		}
	}
}

func TestLimitsMaxDepthOrder(t *testing.T) {
	// Whichever of MaxDepth and the resource limits is hit first in the
	// document is the one that gets reported.
	t.Run("limit first", func(t *testing.T) {
		var v map[string]any
		dec := NewDecoder(strings.NewReader("k1 = 1\nk2 = 2\n[a.b.c]\nk3 = 3\n"))
		dec.MaxDepth(2)
		if err := dec.MaxKeyAssignments(1); err != nil {
			t.Fatal(err)
		}
		_, err := dec.Decode(&v)
		var le LimitError
		if !errors.As(err, &le) || le.Name != "MaxKeyAssignments" {
			t.Errorf("want MaxKeyAssignments error, got %v", err)
		}
	})

	t.Run("depth first", func(t *testing.T) {
		var v map[string]any
		dec := NewDecoder(strings.NewReader("[a.b.c]\nk1 = 1\nk2 = 2\n"))
		dec.MaxDepth(2)
		if err := dec.MaxKeyAssignments(1); err != nil {
			t.Fatal(err)
		}
		_, err := dec.Decode(&v)
		if err == nil || !strings.Contains(err.Error(), "maximum table depth") {
			t.Errorf("want MaxDepth error, got %v", err)
		}
		var le LimitError
		if errors.As(err, &le) {
			t.Errorf("depth error should win, got limit error: %v", le)
		}
	})
}
