package toml

import (
	"reflect"
	"strings"
	"testing"
)

// serviceItem builds a baseline service table with the given id and port.
func serviceItem(id string, port int64) map[string]any {
	return map[string]any{"id": id, "port": port}
}

// serviceIDs returns the ids of the services in the result, in order.
func serviceIDs(t *testing.T, result map[string]any) []string {
	t.Helper()
	arr, ok := result["services"].([]any)
	if !ok {
		t.Fatalf("services is not []any: %T", result["services"])
	}
	ids := make([]string, 0, len(arr))
	for _, e := range arr {
		m := e.(map[string]any)
		ids = append(ids, m["id"].(string))
	}
	return ids
}

// serviceByID returns the service table with the given id from the result.
func serviceByID(t *testing.T, result map[string]any, id string) map[string]any {
	t.Helper()
	arr := result["services"].([]any)
	for _, e := range arr {
		m := e.(map[string]any)
		if m["id"].(string) == id {
			return m
		}
	}
	t.Fatalf("service %q not found", id)
	return nil
}

func TestMergeArrayTables_Acceptance(t *testing.T) {
	baseline := map[string]any{
		"services": []any{
			serviceItem("a", 1),
			serviceItem("b", 2),
		},
	}
	override := map[string]any{
		"services": []any{
			serviceItem("b", 20),
			serviceItem("c", 3),
		},
	}
	opts := MergeOptions{ArrayKey: "services", IdentityField: "id"}

	result, err := MergeArrayTables(baseline, override, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ids := serviceIDs(t, result)
	if got, want := ids, []string{"a", "b", "c"}; len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("order = %v, want %v", got, want)
			}
		}
	}

	// b's port is overridden; a and c keep their ports.
	if got := serviceByID(t, result, "b")["port"]; got != int64(20) {
		t.Fatalf("b port = %v, want 20", got)
	}
	if got := serviceByID(t, result, "a")["port"]; got != int64(1) {
		t.Fatalf("a port = %v, want 1", got)
	}
	if got := serviceByID(t, result, "c")["port"]; got != int64(3) {
		t.Fatalf("c port = %v, want 3", got)
	}
}

func TestMergeArrayTables_Delete(t *testing.T) {
	baseline := map[string]any{
		"services": []any{
			serviceItem("a", 1),
			serviceItem("b", 2),
		},
	}
	override := map[string]any{
		"delete": []any{"a"},
		"services": []any{
			serviceItem("b", 20),
			serviceItem("c", 3),
		},
	}
	opts := MergeOptions{ArrayKey: "services", IdentityField: "id", DeleteField: "delete"}

	result, err := MergeArrayTables(baseline, override, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ids := serviceIDs(t, result)
	if got, want := ids, []string{"b", "c"}; len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("order = %v, want %v", got, want)
			}
		}
	}

	// a is gone; b's port is overridden; c is new.
	if got := serviceByID(t, result, "b")["port"]; got != int64(20) {
		t.Fatalf("b port = %v, want 20", got)
	}
	if got := serviceByID(t, result, "c")["port"]; got != int64(3) {
		t.Fatalf("c port = %v, want 3", got)
	}
	if _, ok := result["delete"]; ok {
		t.Fatalf("delete field should not be copied to result")
	}
}

func TestMergeArrayTables_DuplicateIsError(t *testing.T) {
	baseline := map[string]any{
		"services": []any{
			serviceItem("a", 1),
			serviceItem("b", 2),
		},
	}
	override := map[string]any{
		"services": []any{
			serviceItem("b", 20),
			serviceItem("b", 99),
		},
	}
	opts := MergeOptions{ArrayKey: "services", IdentityField: "id"}

	result, err := MergeArrayTables(baseline, override, opts)
	if err == nil {
		t.Fatalf("expected error for duplicate identity, got result: %v", result)
	}
	if result != nil {
		t.Fatalf("result must be nil on error, got %v", result)
	}
	if !strings.Contains(err.Error(), `duplicate identity "b"`) {
		t.Fatalf("error = %q, want duplicate identity message", err.Error())
	}
	// Both sides' positions should be reported.
	if !strings.Contains(err.Error(), "override array") {
		t.Fatalf("error = %q, want override position", err.Error())
	}
	if !strings.Contains(err.Error(), "baseline array") {
		t.Fatalf("error = %q, want baseline position", err.Error())
	}
}

func TestMergeArrayTables_DuplicateBaselineIsError(t *testing.T) {
	baseline := map[string]any{
		"services": []any{
			serviceItem("b", 1),
			serviceItem("b", 2),
		},
	}
	override := map[string]any{
		"services": []any{
			serviceItem("c", 3),
		},
	}
	opts := MergeOptions{ArrayKey: "services", IdentityField: "id"}

	result, err := MergeArrayTables(baseline, override, opts)
	if err == nil {
		t.Fatalf("expected error for duplicate baseline identity, got result: %v", result)
	}
	if result != nil {
		t.Fatalf("result must be nil on error, got %v", result)
	}
	if !strings.Contains(err.Error(), `duplicate identity "b"`) {
		t.Fatalf("error = %q, want duplicate identity message", err.Error())
	}
}

func TestMergeArrayTables_UnknownDeleteIDIsError(t *testing.T) {
	baseline := map[string]any{
		"services": []any{
			serviceItem("a", 1),
			serviceItem("b", 2),
		},
	}
	override := map[string]any{
		"delete": []any{"zzz"},
		"services": []any{
			serviceItem("b", 20),
		},
	}
	opts := MergeOptions{ArrayKey: "services", IdentityField: "id", DeleteField: "delete"}

	result, err := MergeArrayTables(baseline, override, opts)
	if err == nil {
		t.Fatalf("expected error for unknown delete id, got result: %v", result)
	}
	if result != nil {
		t.Fatalf("result must be nil on error, got %v", result)
	}
	if !strings.Contains(err.Error(), `delete id "zzz"`) {
		t.Fatalf("error = %q, want unknown delete id message", err.Error())
	}
}

func TestMergeArrayTables_MissingIdentityIsError(t *testing.T) {
	baseline := map[string]any{
		"services": []any{
			serviceItem("a", 1),
			map[string]any{"port": 2}, // missing id
		},
	}
	override := map[string]any{
		"services": []any{
			serviceItem("c", 3),
		},
	}
	opts := MergeOptions{ArrayKey: "services", IdentityField: "id"}

	result, err := MergeArrayTables(baseline, override, opts)
	if err == nil {
		t.Fatalf("expected error for missing identity, got result: %v", result)
	}
	if result != nil {
		t.Fatalf("result must be nil on error, got %v", result)
	}
	if !strings.Contains(err.Error(), "missing identity field") {
		t.Fatalf("error = %q, want missing identity message", err.Error())
	}
}

func TestMergeArrayTables_PointerIsolation(t *testing.T) {
	baseline := map[string]any{
		"services": []any{
			map[string]any{
				"id":   "a",
				"port": int64(1),
				"tags": []any{"x"},
				"meta": map[string]any{"env": "prod"},
			},
		},
	}
	override := map[string]any{
		"services": []any{
			map[string]any{
				"id":   "a",
				"port": int64(2),
				"tags": []any{"y", "z"},
				"meta": map[string]any{"region": "eu"},
			},
		},
		"title": "overridden",
	}
	opts := MergeOptions{ArrayKey: "services", IdentityField: "id"}

	baseCopy := deepCopyValue(baseline)
	overCopy := deepCopyValue(override)

	result, err := MergeArrayTables(baseline, override, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Mutate the result; the inputs must remain unchanged.
	result["services"].([]any)[0].(map[string]any)["port"] = int64(999)
	result["services"].([]any)[0].(map[string]any)["tags"].([]any)[0] = "MUTATED"
	result["title"] = "MUTATED"

	if !reflect.DeepEqual(baseline, baseCopy) {
		t.Fatalf("baseline was modified: %v", baseline)
	}
	if !reflect.DeepEqual(override, overCopy) {
		t.Fatalf("override was modified: %v", override)
	}

	// The result must not share slices/maps with the inputs.
	resultTags := result["services"].([]any)[0].(map[string]any)["tags"].([]any)
	baseTags := baseline["services"].([]any)[0].(map[string]any)["tags"].([]any)
	if &resultTags[0] == &baseTags[0] {
		t.Fatalf("result shares backing array with baseline")
	}
}

func TestMergeArrayTables_ArraysReplace(t *testing.T) {
	baseline := map[string]any{
		"services": []any{
			map[string]any{"id": "a", "tags": []any{"x", "y"}},
		},
	}
	override := map[string]any{
		"services": []any{
			map[string]any{"id": "a", "tags": []any{"z"}},
		},
	}
	opts := MergeOptions{ArrayKey: "services", IdentityField: "id"}

	result, err := MergeArrayTables(baseline, override, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tags := serviceByID(t, result, "a")["tags"].([]any)
	if got, want := tags, []any{"z"}; len(got) != len(want) {
		t.Fatalf("tags = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("tags = %v, want %v", got, want)
			}
		}
	}
}

func TestMergeArrayTables_NestedTablesMerge(t *testing.T) {
	baseline := map[string]any{
		"services": []any{
			map[string]any{
				"id":   "a",
				"meta": map[string]any{"env": "prod", "region": "us"},
			},
		},
	}
	override := map[string]any{
		"services": []any{
			map[string]any{
				"id":   "a",
				"meta": map[string]any{"region": "eu"},
			},
		},
	}
	opts := MergeOptions{ArrayKey: "services", IdentityField: "id"}

	result, err := MergeArrayTables(baseline, override, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	meta := serviceByID(t, result, "a")["meta"].(map[string]any)
	if meta["env"] != "prod" {
		t.Fatalf("env = %v, want prod", meta["env"])
	}
	if meta["region"] != "eu" {
		t.Fatalf("region = %v, want eu", meta["region"])
	}
}

func TestMergeArrayTables_EmptyTableNotDeletion(t *testing.T) {
	baseline := map[string]any{
		"services": []any{
			map[string]any{"id": "a", "port": int64(1)},
		},
	}
	override := map[string]any{
		"services": []any{
			map[string]any{"id": "a"}, // empty table: no fields to merge
		},
	}
	opts := MergeOptions{ArrayKey: "services", IdentityField: "id"}

	result, err := MergeArrayTables(baseline, override, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// a's port must survive; an empty table is not a deletion.
	if got := serviceByID(t, result, "a")["port"]; got != int64(1) {
		t.Fatalf("port = %v, want 1 (empty table must not delete)", got)
	}
}

func TestMergeArrayTables_EmptyArrayReplaces(t *testing.T) {
	baseline := map[string]any{
		"services": []any{
			map[string]any{"id": "a", "tags": []any{"x"}},
		},
	}
	override := map[string]any{
		"services": []any{
			map[string]any{"id": "a", "tags": []any{}},
		},
	}
	opts := MergeOptions{ArrayKey: "services", IdentityField: "id"}

	result, err := MergeArrayTables(baseline, override, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tags := serviceByID(t, result, "a")["tags"].([]any)
	if len(tags) != 0 {
		t.Fatalf("tags = %v, want empty (array replaces)", tags)
	}
}

func TestMergeArrayTables_TopLevelMerge(t *testing.T) {
	baseline := map[string]any{
		"title": "baseline",
		"db":    map[string]any{"host": "localhost", "port": int64(5432)},
	}
	override := map[string]any{
		"title": "override",
		"db":    map[string]any{"port": int64(5433)},
	}
	opts := MergeOptions{ArrayKey: "services", IdentityField: "id"}

	result, err := MergeArrayTables(baseline, override, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["title"] != "override" {
		t.Fatalf("title = %v, want override", result["title"])
	}
	db := result["db"].(map[string]any)
	if db["host"] != "localhost" {
		t.Fatalf("db.host = %v, want localhost", db["host"])
	}
	if db["port"] != int64(5433) {
		t.Fatalf("db.port = %v, want 5433", db["port"])
	}
}

func TestMergeArrayTables_ValidationErrors(t *testing.T) {
	tests := []struct {
		name     string
		baseline map[string]any
		override map[string]any
		opts     MergeOptions
		wantErr  string
	}{
		{
			name:     "empty ArrayKey",
			baseline: map[string]any{},
			override: map[string]any{},
			opts:     MergeOptions{IdentityField: "id"},
			wantErr:  "ArrayKey must not be empty",
		},
		{
			name:     "empty IdentityField",
			baseline: map[string]any{},
			override: map[string]any{},
			opts:     MergeOptions{ArrayKey: "services"},
			wantErr:  "IdentityField must not be empty",
		},
		{
			name:     "baseline array is not a table array",
			baseline: map[string]any{"services": "not-an-array"},
			override: map[string]any{},
			opts:     MergeOptions{ArrayKey: "services", IdentityField: "id"},
			wantErr:  "must be an array of tables",
		},
		{
			name:     "override item is not a table",
			baseline: map[string]any{"services": []any{serviceItem("a", 1)}},
			override: map[string]any{"services": []any{"not-a-table"}},
			opts:     MergeOptions{ArrayKey: "services", IdentityField: "id"},
			wantErr:  "must be a table",
		},
		{
			name:     "identity is not a string",
			baseline: map[string]any{"services": []any{map[string]any{"id": 42}}},
			override: map[string]any{},
			opts:     MergeOptions{ArrayKey: "services", IdentityField: "id"},
			wantErr:  "must be a string",
		},
		{
			name:     "delete field is not an array",
			baseline: map[string]any{"services": []any{serviceItem("a", 1)}},
			override: map[string]any{"delete": "a"},
			opts:     MergeOptions{ArrayKey: "services", IdentityField: "id", DeleteField: "delete"},
			wantErr:  "must be an array of strings",
		},
		{
			name:     "delete item is not a string",
			baseline: map[string]any{"services": []any{serviceItem("a", 1)}},
			override: map[string]any{"delete": []any{42}},
			opts:     MergeOptions{ArrayKey: "services", IdentityField: "id", DeleteField: "delete"},
			wantErr:  "must be a string",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := MergeArrayTables(tt.baseline, tt.override, tt.opts)
			if err == nil {
				t.Fatalf("expected error, got result: %v", result)
			}
			if result != nil {
				t.Fatalf("result must be nil on error, got %v", result)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestMergeArrayTables_NilBaseline(t *testing.T) {
	override := map[string]any{
		"services": []any{
			serviceItem("a", 1),
			serviceItem("b", 2),
		},
	}
	opts := MergeOptions{ArrayKey: "services", IdentityField: "id"}

	result, err := MergeArrayTables(nil, override, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ids := serviceIDs(t, result)
	if got, want := ids, []string{"a", "b"}; len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("order = %v, want %v", got, want)
			}
		}
	}
}

func TestMergeArrayTables_WithTOMLDecodedData(t *testing.T) {
	baselineTOML := `
[[services]]
id = "a"
port = 1

[[services]]
id = "b"
port = 2
`
	overrideTOML := `
[[services]]
id = "b"
port = 20

[[services]]
id = "c"
port = 3
`
	var baseline, override map[string]any
	if _, err := Decode(baselineTOML, &baseline); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	if _, err := Decode(overrideTOML, &override); err != nil {
		t.Fatalf("decode override: %v", err)
	}

	opts := MergeOptions{ArrayKey: "services", IdentityField: "id"}
	result, err := MergeArrayTables(baseline, override, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ids := serviceIDs(t, result)
	if got, want := ids, []string{"a", "b", "c"}; len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("order = %v, want %v", got, want)
			}
		}
	}
	if got := serviceByID(t, result, "b")["port"]; got != int64(20) {
		t.Fatalf("b port = %v, want 20", got)
	}
}

func TestMergeArrayTables_DeleteWithTOMLDecodedData(t *testing.T) {
	baselineTOML := `
[[services]]
id = "a"
port = 1

[[services]]
id = "b"
port = 2
`
	overrideTOML := `
delete = ["a"]

[[services]]
id = "b"
port = 20

[[services]]
id = "c"
port = 3
`
	var baseline, override map[string]any
	if _, err := Decode(baselineTOML, &baseline); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	if _, err := Decode(overrideTOML, &override); err != nil {
		t.Fatalf("decode override: %v", err)
	}

	opts := MergeOptions{ArrayKey: "services", IdentityField: "id", DeleteField: "delete"}
	result, err := MergeArrayTables(baseline, override, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ids := serviceIDs(t, result)
	if got, want := ids, []string{"b", "c"}; len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("order = %v, want %v", got, want)
			}
		}
	}
}
