package toml

import (
	"fmt"
	"reflect"
)

// MergeOptions configures [MergeArrayTables].
//
// ArrayKey names the key in the baseline and override maps that holds the
// array of tables to merge. IdentityField names the field inside each table
// whose value identifies the item. DeleteField, when non-empty, names the
// key in the override map whose value is an explicit list of identity
// values to remove from the baseline.
type MergeOptions struct {
	// ArrayKey is the key in the baseline and override maps that holds the
	// array of tables to merge. Required.
	ArrayKey string

	// IdentityField is the field inside each table whose value identifies
	// the item. Items in the baseline and override with the same identity
	// are merged. The value must be a string. Required.
	IdentityField string

	// DeleteField, when non-empty, names the key in the override map whose
	// value is a list of identity values to remove from the baseline before
	// merging. The value must be an array of strings. Deletion is only
	// performed through this explicit list; an empty table or an empty
	// array is never interpreted as a deletion request.
	DeleteField string
}

// MergeArrayTables merges the override map into the baseline map by matching
// items in the array of tables named by opts.ArrayKey on the identity field
// opts.IdentityField.
//
// Baseline items keep their relative order. Override items whose identity is
// not present in the baseline are appended at the end in override order. An
// override item with the same identity as a baseline item is merged into it:
// nested tables are merged recursively, while arrays and scalar values are
// replaced wholesale by the override.
//
// Deletion is performed only through the explicit delete list named by
// opts.DeleteField. An empty table is never treated as a deletion. An
// identity in the delete list that is not present in the baseline is an
// error.
//
// The baseline and override maps, and the maps and slices they contain, are
// never modified. A new map is returned that shares no mutable state with
// either input. No result is returned unless every validation check passes.
func MergeArrayTables(baseline, override map[string]any, opts MergeOptions) (map[string]any, error) {
	if opts.ArrayKey == "" {
		return nil, fmt.Errorf("toml: MergeArrayTables: ArrayKey must not be empty")
	}
	if opts.IdentityField == "" {
		return nil, fmt.Errorf("toml: MergeArrayTables: IdentityField must not be empty")
	}

	baseItems, err := tableItems(baseline[opts.ArrayKey], opts.ArrayKey, "baseline")
	if err != nil {
		return nil, err
	}
	overItems, err := tableItems(override[opts.ArrayKey], opts.ArrayKey, "override")
	if err != nil {
		return nil, err
	}

	baseIdx, err := buildIdentityIndex(baseItems, opts.IdentityField, opts.ArrayKey, "baseline")
	if err != nil {
		return nil, err
	}
	overIdx, err := buildIdentityIndex(overItems, opts.IdentityField, opts.ArrayKey, "override")
	if err != nil {
		return nil, err
	}

	// Duplicate identities within a side are ambiguous and are an error.
	// Report the positions on both sides.
	if err := checkDuplicates(baseItems, opts.IdentityField, overIdx, opts.ArrayKey, "baseline", "override"); err != nil {
		return nil, err
	}
	if err := checkDuplicates(overItems, opts.IdentityField, baseIdx, opts.ArrayKey, "override", "baseline"); err != nil {
		return nil, err
	}

	// Explicit deletion list. Deletion is only performed through this list;
	// an empty table is never interpreted as a deletion.
	deleteSet := map[string]bool{}
	if opts.DeleteField != "" {
		if dv, present := override[opts.DeleteField]; present && dv != nil {
			ids, err := stringList(dv, opts.DeleteField)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				if _, ok := baseIdx[id]; !ok {
					return nil, fmt.Errorf("toml: MergeArrayTables: delete id %q is not present in baseline array %q", id, opts.ArrayKey)
				}
				deleteSet[id] = true
			}
		}
	}

	// All checks passed. Build the result on a deep copy so the inputs are
	// never aliased or modified.
	var result map[string]any
	if baseline == nil {
		result = map[string]any{}
	} else {
		result = deepCopyValue(baseline).(map[string]any)
	}

	// Extract the copied array of tables as []map[string]any.
	resultItems := tableItemsFromCopy(result[opts.ArrayKey])

	// Apply deletes, preserving the order of the remaining items.
	if len(deleteSet) > 0 {
		kept := resultItems[:0]
		for _, item := range resultItems {
			if !deleteSet[item[opts.IdentityField].(string)] {
				kept = append(kept, item)
			}
		}
		resultItems = kept
	}

	// Rebuild the identity index after deletion so override items merge
	// into the correct (possibly shifted) positions.
	resultIdx := make(map[string]int, len(resultItems))
	for i, item := range resultItems {
		resultIdx[item[opts.IdentityField].(string)] = i
	}

	// Merge override items in order. Items with a surviving baseline
	// identity are merged; new items are appended.
	for _, oItem := range overItems {
		id := oItem[opts.IdentityField].(string)
		if pos, ok := resultIdx[id]; ok {
			mergeTables(resultItems[pos], oItem)
		} else {
			resultItems = append(resultItems, deepCopyValue(oItem).(map[string]any))
		}
	}

	// Write the merged array back into the result.
	outArr := make([]any, len(resultItems))
	for i, item := range resultItems {
		outArr[i] = item
	}
	result[opts.ArrayKey] = outArr

	// Apply remaining top-level override keys. Nested tables merge
	// recursively; arrays and scalars replace. The array key (already
	// handled) and the delete field (a control instruction) are excluded.
	for k, sv := range override {
		if k == opts.ArrayKey || k == opts.DeleteField {
			continue
		}
		dv, ok := result[k]
		if !ok {
			result[k] = deepCopyValue(sv)
			continue
		}
		dm, dok := dv.(map[string]any)
		sm, sok := sv.(map[string]any)
		if dok && sok {
			mergeTables(dm, sm)
		} else {
			result[k] = deepCopyValue(sv)
		}
	}

	return result, nil
}

// tableItems validates that v is an array of tables and returns its elements.
// A nil or missing value yields an empty (nil) slice.
func tableItems(v any, arrayKey, side string) ([]map[string]any, error) {
	if v == nil {
		return nil, nil
	}
	switch arr := v.(type) {
	case []map[string]any:
		return arr, nil
	case []any:
		items := make([]map[string]any, len(arr))
		for i, e := range arr {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("toml: MergeArrayTables: %s key %q item %d must be a table, got %T", side, arrayKey, i, e)
			}
			items[i] = m
		}
		return items, nil
	default:
		return nil, fmt.Errorf("toml: MergeArrayTables: %s key %q must be an array of tables, got %T", side, arrayKey, v)
	}
}

// tableItemsFromCopy extracts an array of tables from a deep-copied value.
func tableItemsFromCopy(v any) []map[string]any {
	if v == nil {
		return nil
	}
	switch arr := v.(type) {
	case []map[string]any:
		return arr
	case []any:
		items := make([]map[string]any, len(arr))
		for i, e := range arr {
			items[i] = e.(map[string]any)
		}
		return items
	default:
		return nil
	}
}

// buildIdentityIndex builds an index from identity value to position. It
// reports missing and non-string identity fields. Duplicate identities are
// intentionally not reported here; [checkDuplicates] reports them with
// positions on both sides.
func buildIdentityIndex(items []map[string]any, identityField, arrayKey, side string) (map[string]int, error) {
	idx := make(map[string]int, len(items))
	for i, item := range items {
		idv, ok := item[identityField]
		if !ok {
			return nil, fmt.Errorf("toml: MergeArrayTables: %s array %q item %d is missing identity field %q", side, arrayKey, i, identityField)
		}
		id, ok := idv.(string)
		if !ok {
			return nil, fmt.Errorf("toml: MergeArrayTables: %s array %q item %d identity field %q must be a string, got %T", side, arrayKey, i, identityField, idv)
		}
		idx[id] = i
	}
	return idx, nil
}

// checkDuplicates reports duplicate identities within items, including the
// positions on both sides when the identity also appears in the other side.
func checkDuplicates(items []map[string]any, identityField string, otherIdx map[string]int, arrayKey, side, otherSide string) error {
	seen := make(map[string]int, len(items))
	for i, item := range items {
		id := item[identityField].(string)
		if prev, ok := seen[id]; ok {
			msg := fmt.Sprintf("toml: MergeArrayTables: duplicate identity %q in %s array %q at indexes %d and %d",
				id, side, arrayKey, prev, i)
			if oi, ok := otherIdx[id]; ok {
				msg += fmt.Sprintf(" (also present in %s array %q at index %d)", otherSide, arrayKey, oi)
			}
			return fmt.Errorf("%s", msg)
		}
		seen[id] = i
	}
	return nil
}

// stringList validates that v is an array of strings and returns it.
func stringList(v any, deleteField string) ([]string, error) {
	switch arr := v.(type) {
	case []string:
		return arr, nil
	case []any:
		out := make([]string, len(arr))
		for i, e := range arr {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("toml: MergeArrayTables: delete field %q item %d must be a string, got %T", deleteField, i, e)
			}
			out[i] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("toml: MergeArrayTables: delete field %q must be an array of strings, got %T", deleteField, v)
	}
}

// mergeTables merges src into dst recursively. Nested tables are merged
// key-by-key; arrays and scalar values are replaced wholesale. Values copied
// from src are deep-copied so that dst shares no mutable state with src. An
// empty table merges nothing and therefore never deletes anything.
func mergeTables(dst, src map[string]any) {
	for k, sv := range src {
		dv, ok := dst[k]
		if !ok {
			dst[k] = deepCopyValue(sv)
			continue
		}
		dm, dok := dv.(map[string]any)
		sm, sok := sv.(map[string]any)
		if dok && sok {
			mergeTables(dm, sm)
		} else {
			dst[k] = deepCopyValue(sv)
		}
	}
}

// deepCopyValue returns a deep copy of v. Maps and slices (and pointers) are
// copied recursively so that the result shares no mutable state with the
// input. Scalar values are returned as-is.
func deepCopyValue(v any) any {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, vv := range t {
			m[k] = deepCopyValue(vv)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, vv := range t {
			s[i] = deepCopyValue(vv)
		}
		return s
	case []map[string]any:
		s := make([]map[string]any, len(t))
		for i, vv := range t {
			s[i] = deepCopyValue(vv).(map[string]any)
		}
		return s
	default:
		return deepCopyReflect(v)
	}
}

// deepCopyReflect copies map, slice and pointer values of arbitrary types
// using reflection. It is a fallback for types not covered by the type switch
// in [deepCopyValue].
func deepCopyReflect(v any) any {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return v
		}
		m := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			m.SetMapIndex(iter.Key(), reflect.ValueOf(deepCopyValue(iter.Value().Interface())))
		}
		return m.Interface()
	case reflect.Slice:
		s := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			s.Index(i).Set(reflect.ValueOf(deepCopyValue(rv.Index(i).Interface())))
		}
		return s.Interface()
	case reflect.Ptr:
		if rv.IsNil() {
			return v
		}
		p := reflect.New(rv.Elem().Type())
		p.Elem().Set(reflect.ValueOf(deepCopyValue(rv.Elem().Interface())))
		return p.Interface()
	default:
		return v
	}
}
