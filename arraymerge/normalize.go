package arraymerge

import "reflect"

// Normalize converts a configuration decoded by generic decoders (notably
// toml.Decode into *map[string]interface{}) into the uniform shape consumed
// by Merge:
//
//   - map-like values become map[string]interface{}
//   - slice-like values become []interface{}
//
// toml.Decode emits typed slices such as []map[string]interface{} and
// []int64 for arrays of tables / arrays of primitives; Merge works on the
// uniform []interface{} / map[string]interface{} shapes, so callers feeding
// raw decoder output through should Normalize baseline and override first.
// The returned value shares no map or slice storage with the input.
func Normalize(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		out := map[string]interface{}{}
		iter := rv.MapRange()
		for iter.Next() {
			out[iter.Key().String()] = Normalize(iter.Value().Interface())
		}
		return out
	case reflect.Slice, reflect.Array:
		out := make([]interface{}, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = Normalize(rv.Index(i).Interface())
		}
		return out
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return Normalize(rv.Elem().Interface())
	default:
		return v
	}
}

// NormalizeConfig is the typed convenience wrapper around Normalize for a
// decoded configuration table.
func NormalizeConfig(m map[string]interface{}) map[string]interface{} {
	return Normalize(m).(map[string]interface{})
}
