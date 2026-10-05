// Package arraymerge merges an "override" configuration into a "baseline"
// configuration with array-of-tables semantics.
//
// Configurations are plain Go values produced by (for example) toml.Decode:
//   - tables are map[string]interface{}
//   - arrays are []interface{}
//   - leaves are string, int64, float64, bool, time.Time, ...
//
// # Merge semantics
//
// An identity array is a []interface{} of map[string]interface{} whose table
// name was declared with an identity field via Merger.WithIdentity. Items are
// matched by their identity field value:
//
//   - Items present only in the baseline keep their original position.
//   - Items with the same identity in the override recursively merge with the
//     baseline item (maps merge key-by-key, ordinary arrays are replaced
//     wholesale, nested identity arrays follow these same rules).
//   - Items present only in the override are appended to the end, in override
//     order.
//
// An ordinary array is replaced wholesale by the override value. An empty
// override array is therefore NOT interpreted as a deletion: when the
// override has no elements the baseline array is kept unchanged. Items are
// removed only through the explicit delete-ID list passed to Merge (which
// applies to identity arrays), so deleting an item never happens implicitly.
//
// All structural problems are collected during a validation phase and
// reported together; Merge only returns a new configuration when every check
// passes. Neither the baseline nor the override are modified.
package arraymerge

import (
	"fmt"
	"strings"
	"time"
)

const pathSep = "."

// MergeError describes a single structural problem found while merging.
type MergeError struct {
	// Location is the dotted path to the offending array table, e.g.
	// "top.services".
	Location string
	// Detail describes the problem.
	Detail string
}

func (e *MergeError) Error() string {
	if e.Location == "" {
		return e.Detail
	}
	return e.Location + ": " + e.Detail
}

// MultiError is returned when more than one problem is found. Every detected
// problem is reported so that duplicate or missing identities (and where they
// occur on both sides) are all visible in a single run.
type MultiError []*MergeError

func (m MultiError) Error() string {
	parts := make([]string, len(m))
	for i, e := range m {
		parts[i] = e.Error()
	}
	return "arraymerge: " + fmt.Sprintf("%d", len(m)) + " merge error(s):\n\t" + strings.Join(parts, "\n\t")
}

// Merger validates and merges baseline and override configurations.
//
// Identity fields are declared per array-table name (the last component of
// the dotted path). Only the last component is used, so "services" matches
// the array at "services" and "top.services" identically.
type Merger struct {
	identities map[string]string
}

// New returns a Merger with no identity declarations. Declare identity fields
// with WithIdentity before calling Merge.
func New() *Merger {
	return &Merger{identities: map[string]string{}}
}

// WithIdentity declares that array tables named name key their items by the
// value of field idField. It returns the receiver for chaining. Declaring an
// identity twice, or with an empty field name, panics as that is a
// programmer error rather than a data problem.
func (m *Merger) WithIdentity(name, idField string) *Merger {
	if name == "" || idField == "" {
		panic("arraymerge: table name and identity field must both be non-empty")
	}
	if _, exists := m.identities[name]; exists {
		panic("arraymerge: identity for table " + name + " already declared")
	}
	m.identities[name] = idField
	return m
}

// Merge applies override to baseline using the declared identity fields and
// deletes identity-array items whose identity is listed in deletes
// (delete-ID lists are keyed by the array table name, matching WithIdentity).
//
// On success the returned value is a freshly built configuration; baseline
// and override, together with all their reachable maps/slices, are left
// untouched. If any validation fails, Merge returns nil together with every
// error found (a *MergeError or MultiError) and must not be treated as a
// partial result.
func (m *Merger) Merge(baseline, override map[string]interface{}, deletes map[string][]string) (map[string]interface{}, error) {
	errs := m.validate(baseline, override, deletes)
	if len(errs) > 0 {
		if len(errs) == 1 {
			return nil, errs[0]
		}
		return nil, MultiError(errs)
	}

	base := deepClone(baseline).(map[string]interface{})
	over := deepClone(override).(map[string]interface{})
	merged, _ := mergeMaps(base, over, m.identities)
	for table, ids := range deletes {
		idField := m.identities[table]
		for _, id := range ids {
			// Validation already proved every delete ID exists at some path;
			// this therefore removes at least one item.
			merged = deleteByID(merged, table, idField, id)
		}
	}
	return merged, nil
}

// validate walks both configurations and records every structural problem
// before any merging happens.
func (m *Merger) validate(baseline, override map[string]interface{}, deletes map[string][]string) []*MergeError {
	var errs []*MergeError
	m.checkMaps(baseline, override, "", &errs)

	for table, ids := range deletes {
		idField, ok := m.identities[table]
		if !ok {
			errs = append(errs, &MergeError{
				Location: table,
				Detail:   fmt.Sprintf("delete list for unknown table %q (no identity field declared)", table),
			})
			continue
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if id == "" {
				errs = append(errs, &MergeError{
					Location: table,
					Detail:   "delete list contains an empty ID",
				})
				continue
			}
			if seen[id] {
				continue // a repeated delete is harmless; ignore silently
			}
			seen[id] = true
			if !idExists(baseline, override, table, idField, id) {
				errs = append(errs, &MergeError{
					Location: table,
					Detail:   fmt.Sprintf("delete ID %q not found in baseline or override for identity field %q", id, idField),
				})
			}
		}
	}
	return errs
}

// checkMaps recursively compares a baseline/override table pair.
func (m *Merger) checkMaps(baseline, override map[string]interface{}, path string, errs *[]*MergeError) {
	for key, ov := range override {
		loc := joinPath(path, key)
		bv, inBase := baseline[key]
		if !inBase {
			// Pure addition: validate the override subtree internally (a new
			// identity array must not contain duplicate or missing IDs).
			m.checkNewValue(ov, loc, errs)
			continue
		}
		m.checkPair(bv, ov, loc, errs)
	}
}

// checkPair validates matching baseline/override values of the same key.
func (m *Merger) checkPair(bv, ov interface{}, loc string, errs *[]*MergeError) {
	bm, bMap := bv.(map[string]interface{})
	om, oMap := ov.(map[string]interface{})
	bs, bSlice := bv.([]interface{})
	os, oSlice := ov.([]interface{})

	switch {
	case bMap && oMap:
		m.checkMaps(bm, om, loc, errs)
	case bSlice && oSlice:
		m.checkSlices(bs, os, loc, errs)
	case bSlice != oSlice, bMap != oMap:
		*errs = append(*errs, &MergeError{
			Location: loc,
			Detail:   fmt.Sprintf("cannot merge %s into %s", kind(ov), kind(bv)),
		})
	default:
		// Scalar/leaf override: nothing structural to validate.
	}
}

// checkNewValue validates a subtree that exists only in the override.
func (m *Merger) checkNewValue(v interface{}, loc string, errs *[]*MergeError) {
	switch tv := v.(type) {
	case map[string]interface{}:
		for k, child := range tv {
			m.checkNewValue(child, joinPath(loc, k), errs)
		}
	case []interface{}:
		if idField, ok := m.identities[lastPath(loc)]; ok {
			m.checkIdentityShape(tv, idField, loc, errs)
		}
	}
}

// checkSlices validates one baseline/override array pair.
func (m *Merger) checkSlices(baseline, override []interface{}, loc string, errs *[]*MergeError) {
	idField, isIdentity := m.identities[lastPath(loc)]

	if isIdentity {
		bad := false
		for i, item := range baseline {
			if _, ok := item.(map[string]interface{}); !ok {
				*errs = append(*errs, &MergeError{
					Location: loc,
					Detail:   fmt.Sprintf("baseline item %d is %s, expected a table with identity field %q", i, kind(item), idField),
				})
				bad = true
			}
		}
		for i, item := range override {
			if _, ok := item.(map[string]interface{}); !ok {
				*errs = append(*errs, &MergeError{
					Location: loc,
					Detail:   fmt.Sprintf("override item %d is %s, expected a table with identity field %q", i, kind(item), idField),
				})
				bad = true
			}
		}
		if !bad {
			m.checkIdentitySide(baseline, idField, loc, "baseline", errs)
			m.checkIdentitySide(override, idField, loc, "override", errs)
		}
		return
	}

	// Ordinary arrays: recurse into element pairs that are tables/slices so
	// nested identity arrays inside them are validated as well.
	n := len(baseline)
	if len(override) < n {
		n = len(override)
	}
	for i := 0; i < n; i++ {
		m.checkPair(baseline[i], override[i], fmt.Sprintf("%s[%d]", loc, i), errs)
	}
	for i := n; i < len(override); i++ {
		m.checkNewValue(override[i], fmt.Sprintf("%s[%d]", loc, i), errs)
	}
}

// checkIdentityShape validates an identity array that exists on one side
// only, then validates its identity values.
func (m *Merger) checkIdentityShape(items []interface{}, idField, loc string, errs *[]*MergeError) {
	bad := false
	for i, item := range items {
		if _, ok := item.(map[string]interface{}); !ok {
			*errs = append(*errs, &MergeError{
				Location: loc,
				Detail:   fmt.Sprintf("item %d is %s, expected a table with identity field %q", i, kind(item), idField),
			})
			bad = true
		}
	}
	if !bad {
		m.checkIdentitySide(items, idField, loc, "override", errs)
	}
}

// checkIdentitySide reports missing, empty or duplicate identity values in
// one side of an identity array. Each occurrence is reported with its
// position, so duplicates on both sides can be located precisely.
func (m *Merger) checkIdentitySide(items []interface{}, idField, loc, side string, errs *[]*MergeError) {
	positions := map[string][]int{}
	for i, raw := range items {
		item := raw.(map[string]interface{})
		v, present := item[idField]
		if !present {
			*errs = append(*errs, &MergeError{
				Location: loc,
				Detail:   fmt.Sprintf("%s item %d is missing identity field %q", side, i, idField),
			})
			continue
		}
		id, ok := v.(string)
		if !ok || id == "" {
			*errs = append(*errs, &MergeError{
				Location: loc,
				Detail:   fmt.Sprintf("%s item %d has non-string or empty identity field %q (%v)", side, i, idField, v),
			})
			continue
		}
		positions[id] = append(positions[id], i)
	}
	for id, ps := range positions {
		if len(ps) > 1 {
			*errs = append(*errs, &MergeError{
				Location: loc,
				Detail:   fmt.Sprintf("duplicate identity %q in %s at items %v", id, side, ps),
			})
		}
	}
}

// idExists reports whether id occurs in any identity array named table within
// baseline or override.
func idExists(baseline, override map[string]interface{}, table, idField, id string) bool {
	return findIDInMap(baseline, table, idField, id) || findIDInMap(override, table, idField, id)
}

func findIDInMap(v map[string]interface{}, table, idField, id string) bool {
	for key, child := range v {
		if key == table {
			if arr, ok := child.([]interface{}); ok {
				for _, raw := range arr {
					if item, ok := raw.(map[string]interface{}); ok {
						if cur, ok := item[idField].(string); ok && cur == id {
							return true
						}
					}
				}
			}
		}
		switch tv := child.(type) {
		case map[string]interface{}:
			if findIDInMap(tv, table, idField, id) {
				return true
			}
		case []interface{}:
			if findIDInSlice(tv, table, idField, id) {
				return true
			}
		}
	}
	return false
}

func findIDInSlice(items []interface{}, table, idField, id string) bool {
	for _, raw := range items {
		switch tv := raw.(type) {
		case map[string]interface{}:
			if findIDInMap(tv, table, idField, id) {
				return true
			}
		case []interface{}:
			if findIDInSlice(tv, table, idField, id) {
				return true
			}
		}
	}
	return false
}

// deleteByID removes items with identity id from identity arrays named table
// anywhere in v. It returns the (possibly rebuilt) root map.
func deleteByID(v map[string]interface{}, table, idField, id string) map[string]interface{} {
	for key, child := range v {
		switch tv := child.(type) {
		case map[string]interface{}:
			v[key] = deleteByID(tv, table, idField, id)
		case []interface{}:
			v[key] = deleteInSlice(tv, key, table, idField, id)
		}
	}
	return v
}

func deleteInSlice(items []interface{}, key, table, idField, id string) []interface{} {
	if key == table {
		out := make([]interface{}, 0, len(items))
		for _, raw := range items {
			keep := true
			if item, ok := raw.(map[string]interface{}); ok {
				if cur, ok := item[idField].(string); ok && cur == id {
					keep = false
				}
			}
			if keep {
				out = append(out, raw)
			}
		}
		items = out
	}
	for i, raw := range items {
		switch tv := raw.(type) {
		case map[string]interface{}:
			items[i] = deleteByID(tv, table, idField, id)
		case []interface{}:
			items[i] = deleteInSlice(tv, "", table, idField, id)
		}
	}
	return items
}

// mergeMaps merges override into baseline in place (both values are private
// deep clones).
func mergeMaps(baseline, override map[string]interface{}, identities map[string]string) (map[string]interface{}, bool) {
	for key, ov := range override {
		bv, exists := baseline[key]
		if !exists {
			baseline[key] = ov
			continue
		}
		merged, ok := mergeValue(bv, ov, key, identities)
		if ok {
			baseline[key] = merged
		}
	}
	return baseline, true
}

// mergeValue merges an override value into its baseline counterpart. ok is
// false when the types are incompatible (validation rejects this earlier).
func mergeValue(bv, ov interface{}, key string, identities map[string]string) (interface{}, bool) {
	bm, bMap := bv.(map[string]interface{})
	om, oMap := ov.(map[string]interface{})
	bs, bSlice := bv.([]interface{})
	os, oSlice := ov.([]interface{})

	switch {
	case bMap && oMap:
		return mergeMaps(bm, om, identities)
	case bSlice && oSlice:
		if idField, ok := identities[key]; ok {
			return mergeIdentity(bs, os, idField, identities), true
		}
		// Ordinary array: replace wholesale. An empty override array is never
		// a deletion, so an empty override means "leave baseline unchanged".
		if len(os) == 0 {
			return bs, true
		}
		// Recursively merge matching element pairs so nested identity arrays
		// inside ordinary arrays still merge; surplus override elements append.
		return mergeOrdinary(bs, os, identities), true
	default:
		if bMap != oMap || bSlice != oSlice {
			return nil, false
		}
		return ov, true
	}
}

// mergeOrdinary replaces a non-identity array with the override, while still
// recursing into table/array element pairs for nested identity semantics.
func mergeOrdinary(bs, os []interface{}, identities map[string]string) []interface{} {
	out := make([]interface{}, len(os))
	copy(out, os)
	n := len(bs)
	if len(os) < n {
		n = len(os)
	}
	for i := 0; i < n; i++ {
		if merged, ok := mergeValue(bs[i], os[i], "", identities); ok {
			out[i] = merged
		}
	}
	return out
}

// mergeIdentity merges two sides of an identity array: baseline order is
// preserved and new override items are appended at the end.
func mergeIdentity(baseline, override []interface{}, idField string, identities map[string]string) []interface{} {
	indexByID := map[string]int{}
	order := make([]interface{}, 0, len(baseline)+len(override))

	// Start from baseline items in baseline order.
	for _, raw := range baseline {
		item := raw.(map[string]interface{})
		id := item[idField].(string)
		indexByID[id] = len(order)
		order = append(order, item)
	}

	// Apply override: existing items recurse, new items append.
	for _, raw := range override {
		ovItem := raw.(map[string]interface{})
		id := ovItem[idField].(string)
		if i, found := indexByID[id]; found {
			baseItem := order[i].(map[string]interface{})
			merged, _ := mergeMaps(baseItem, ovItem, identities)
			order[i] = merged
		} else {
			indexByID[id] = len(order)
			order = append(order, ovItem)
		}
	}
	return order
}

// deepClone returns a value whose maps and slices share no storage with v.
// This provides pointer isolation: callers may mutate the result and the
// original baseline/override stay byte-for-byte unchanged.
func deepClone(v interface{}) interface{} {
	switch tv := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(tv))
		for k, child := range tv {
			out[k] = deepClone(child)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(tv))
		for i, child := range tv {
			out[i] = deepClone(child)
		}
		return out
	default:
		// Strings, numbers, bools, time.Time, nil and other leaf values are
		// immutable (or treated as such), so no copy is needed.
		return v
	}
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + pathSep + key
}

func lastPath(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == pathSep[0] {
			return path[i+1:]
		}
	}
	return path
}

func kind(v interface{}) string {
	switch v.(type) {
	case map[string]interface{}:
		return "table"
	case []interface{}:
		return "array"
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "bool"
	case int, int64, float64:
		return "number"
	case time.Time:
		return "datetime"
	default:
		return fmt.Sprintf("%T", v)
	}
}
