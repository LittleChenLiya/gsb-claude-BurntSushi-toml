package toml

import (
	"fmt"
	"reflect"
	"strings"
)

// KeyMigration describes the rename of a single key, applied to the parsed
// TOML data before it is decoded with [Decoder.DecodeWithKeyMigrations].
//
// From and To are key paths: every element is exactly one key segment, so a
// key that literally contains a dot (such as the quoted key "a.b") is a
// single segment and is never confused with a nested path.
//
// A single "*" segment matches every element of an array of tables at that
// position and migrates each element individually. When used, it must appear
// in both From and To at the same position, may not be the last segment, and
// the path leading up to it must be identical in From and To.
//
// Migrations are applied in order. A migration whose source key doesn't
// exist is a no-op. The following are reported as errors:
//
//   - both the old and new key exist (no silent overwrites);
//   - the target is below a scalar value or inside an array of tables
//     without a "*" segment;
//   - chained (a->b and b->c), cyclic (a->b and b->a), or duplicate
//     migrations;
//   - the target key doesn't match any field of the destination struct.
//
// The input TOML data is never modified; migrations only apply to the parsed
// representation of a single Decode call.
type KeyMigration struct {
	From Key // Old key path.
	To   Key // New key path.
}

// Migration describes a single key migration that was applied by
// [Decoder.DecodeWithKeyMigrations]. One [KeyMigration] with a "*" segment
// can produce multiple Migrations: one per array element.
type Migration struct {
	From     Key      // Old key path.
	To       Key      // New key path.
	Index    int      // Array of tables element index for "*" migrations; -1 otherwise.
	Position Position // Position of the source key in the TOML input.
	Field    string   // Struct field the target key maps to; "" for maps or any.
}

// DecodeWithKeyMigrations decodes TOML data like [Decoder.Decode], but
// applies the given key migrations to the parsed data before decoding it.
//
// It returns the list of applied migrations, including the source position
// and target struct field of every migrated key. The regular
// [Decoder.Decode] never applies key migrations.
//
// See [KeyMigration] for the migration rules and conflict handling.
func (dec *Decoder) DecodeWithKeyMigrations(v any, migrations ...KeyMigration) (MetaData, []Migration, error) {
	return dec.decode(v, migrations)
}

// wildcardIndex returns the index of the "*" segment in k, or -1.
func wildcardIndex(k Key) int {
	for i, s := range k {
		if s == "*" {
			return i
		}
	}
	return -1
}

func wildcardCount(k Key) int {
	n := 0
	for _, s := range k {
		if s == "*" {
			n++
		}
	}
	return n
}

func equalKeys(a, b Key) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// validateKeyMigrations checks a set of key migrations for duplicates,
// chains, cycles, and invalid wildcard usage.
func validateKeyMigrations(ms []KeyMigration) error {
	froms := make(map[string]Key, len(ms))
	for _, m := range ms {
		if len(m.From) == 0 {
			return fmt.Errorf("toml: key migration to %q has an empty source path", m.To)
		}
		if len(m.To) == 0 {
			return fmt.Errorf("toml: key migration from %q has an empty target path", m.From)
		}
		if equalKeys(m.From, m.To) {
			return fmt.Errorf("toml: key migration %q migrates to itself", m.From)
		}
		if n := wildcardCount(m.From); n > 1 || wildcardCount(m.To) > 1 {
			return fmt.Errorf("toml: key migration %q -> %q uses more than one \"*\" segment; only a single wildcard is supported", m.From, m.To)
		} else if n != wildcardCount(m.To) {
			return fmt.Errorf("toml: key migration %q -> %q must use a \"*\" segment in both paths or in neither", m.From, m.To)
		}
		if wi := wildcardIndex(m.From); wi >= 0 {
			if wi == len(m.From)-1 || wildcardIndex(m.To) == len(m.To)-1 {
				return fmt.Errorf("toml: key migration %q -> %q: \"*\" may not be the last path segment", m.From, m.To)
			}
			if wildcardIndex(m.To) != wi || !equalKeys(m.From[:wi], m.To[:wi]) {
				return fmt.Errorf("toml: key migration %q -> %q: the path up to \"*\" must be identical in the source and target", m.From, m.To)
			}
		}
		fs := m.From.String()
		if _, dup := froms[fs]; dup {
			return fmt.Errorf("toml: duplicate key migration from %q", m.From)
		}
		froms[fs] = m.From
	}
	for _, m := range ms {
		if from, ok := froms[m.To.String()]; ok {
			return fmt.Errorf("toml: key migration %q -> %q conflicts with migration from %q: chained and cyclic key migrations are not supported",
				m.From, m.To, from)
		}
	}
	return nil
}

// migrator applies key migrations to freshly parsed TOML data.
type migrator struct {
	p    *parser
	data string // Input file; for error positions.
}

func (mg *migrator) err(pos Position, format string, args ...any) error {
	return ParseError{
		Message:  fmt.Sprintf(format, args...),
		Position: pos.withCol(mg.data),
		Line:     pos.Line,
		input:    mg.data,
	}
}

// posOf returns the position of the key sub (relative to the table the
// migrator is working on). With a nil idxPrefix the key is looked up in
// keyInfo; otherwise idxPrefix is an index-qualified prefix such as
// ["servers[1]"] and the key is looked up in arrayKeyInfo.
func (mg *migrator) posOf(idxPrefix []string, sub Key) Position {
	if len(idxPrefix) == 0 {
		return mg.p.keyInfo[sub.String()].pos
	}
	parts := make([]string, 0, len(idxPrefix)+len(sub))
	parts = append(parts, idxPrefix...)
	for _, s := range sub {
		parts = append(parts, Key{s}.String())
	}
	return mg.p.arrayKeyInfo[strings.Join(parts, ".")].pos
}

// apply runs a single key migration, returning the applied migrations.
func (mg *migrator) apply(m KeyMigration) ([]Migration, error) {
	if wi := wildcardIndex(m.From); wi >= 0 {
		return mg.applyWildcard(m, wi)
	}
	mig, found, err := mg.migrateIn(mg.p.mapping, m.From, m.To, nil, -1)
	if err != nil || !found {
		return nil, err
	}
	replaceOrdered(mg.p, m.From, m.To)
	return []Migration{*mig}, nil
}

// applyWildcard applies a migration with a "*" segment to every element of
// an array of tables.
func (mg *migrator) applyWildcard(m KeyMigration, wi int) ([]Migration, error) {
	prefix := m.From[:wi]

	// Find the array of tables.
	table := mg.p.mapping
	for _, seg := range prefix[:len(prefix)-1] {
		t, ok := table[seg].(map[string]any)
		if !ok {
			return nil, nil // Source doesn't exist: no-op.
		}
		table = t
	}
	arr, ok := table[prefix[len(prefix)-1]].([]map[string]any)
	if !ok {
		if table[prefix[len(prefix)-1]] == nil {
			return nil, nil // Source doesn't exist: no-op.
		}
		return nil, mg.err(mg.posOf(nil, prefix),
			"cannot migrate key %q to %q: %q is not an array of tables", m.From, m.To, prefix)
	}

	fromRest, toRest := m.From[wi+1:], m.To[wi+1:]
	fromFull := append(append(Key{}, prefix...), fromRest...)
	toFull := append(append(Key{}, prefix...), toRest...)

	var applied []Migration
	for i := range arr {
		idxPrefix := indexedPrefix(prefix, i)
		mig, found, err := mg.migrateIn(arr[i], fromRest, toRest, idxPrefix, i)
		if err != nil {
			if perr, ok := err.(ParseError); ok {
				perr.Message = fmt.Sprintf("element %d of %q: %s", i+1, prefix, perr.Message)
				return nil, perr
			}
			return nil, fmt.Errorf("element %d of %q: %w", i+1, prefix, err)
		}
		if !found {
			continue
		}
		// Register the full target key as well, so that decode-time errors
		// (e.g. integer overflows) point at the source key.
		mg.p.keyInfo[toFull.String()] = mg.p.arrayKeyInfo[strings.Join(append(idxPrefix, quoteKey(fromRest)...), ".")]
		mig.From, mig.To = m.From, m.To
		applied = append(applied, *mig)
	}
	if len(applied) > 0 {
		replaceOrdered(mg.p, fromFull, toFull)
	}
	return applied, nil
}

// indexedPrefix renders an index-qualified prefix such as ["servers[1]"] for
// the i-th element of the array of tables at the end of prefix.
func indexedPrefix(prefix Key, i int) []string {
	parts := make([]string, 0, len(prefix))
	for _, s := range prefix[:len(prefix)-1] {
		parts = append(parts, Key{s}.String())
	}
	return append(parts, fmt.Sprintf("%s[%d]", Key{prefix[len(prefix)-1]}.String(), i))
}

// migrateIn moves the key "from" to "to" within table. Both paths are
// relative to table and contain no "*" segments. idxPrefix is the
// index-qualified prefix of table (nil at the top level) and idx the array
// element index (-1 at the top level).
func (mg *migrator) migrateIn(table map[string]any, from, to Key, idxPrefix []string, idx int) (*Migration, bool, error) {
	// Find the source key.
	srcParent := table
	for _, seg := range from[:len(from)-1] {
		v, ok := srcParent[seg]
		if !ok {
			return nil, false, nil
		}
		if srcParent, ok = v.(map[string]any); !ok {
			return nil, false, nil // Path passes through a non-table: the key doesn't exist.
		}
	}
	srcLast := from[len(from)-1]
	val, ok := srcParent[srcLast]
	if !ok {
		return nil, false, nil
	}

	// Find or create the target table.
	dstParent := table
	for i, seg := range to[:len(to)-1] {
		v, ok := dstParent[seg]
		if !ok {
			nm := make(map[string]any)
			dstParent[seg] = nm
			dstParent = nm
			continue
		}
		switch t := v.(type) {
		case map[string]any:
			dstParent = t
		case []map[string]any:
			return nil, false, mg.err(mg.posOf(idxPrefix, to[:i+1]),
				"cannot migrate key %q to %q: %q is an array of tables; use a \"*\" segment to migrate its elements",
				from, to, to[:i+1])
		default:
			return nil, false, mg.err(mg.posOf(idxPrefix, to[:i+1]),
				"cannot migrate key %q to %q: %q is a %s, not a table",
				from, to, to[:i+1], fmtType(v))
		}
	}

	dstLast := to[len(to)-1]
	if _, ok := dstParent[dstLast]; ok {
		return nil, false, mg.err(mg.posOf(idxPrefix, to),
			"cannot migrate key %q to %q: both keys exist; refusing to silently overwrite %q",
			from, to, to)
	}

	// Move the value and record the new key info.
	dstParent[dstLast] = val
	delete(srcParent, srcLast)
	mg.recordKey(idxPrefix, from, to)

	return &Migration{
		From:     append(Key{}, from...),
		To:       append(Key{}, to...),
		Index:    idx,
		Position: mg.posOf(idxPrefix, from),
	}, true, nil
}

// recordKey registers key info for the migrated target key, keeping error
// positions (e.g. for type errors found during decoding) pointed at the
// source key.
func (mg *migrator) recordKey(idxPrefix []string, from, to Key) {
	if len(idxPrefix) == 0 {
		mg.p.keyInfo[to.String()] = mg.p.keyInfo[from.String()]
		return
	}
	fromIdx := append(append([]string{}, idxPrefix...), quoteKey(from)...)
	toIdx := append(append([]string{}, idxPrefix...), quoteKey(to)...)
	mg.p.arrayKeyInfo[strings.Join(toIdx, ".")] = mg.p.arrayKeyInfo[strings.Join(fromIdx, ".")]
}

// quoteKey renders every segment of k like Key.String renders a single
// segment.
func quoteKey(k Key) []string {
	parts := make([]string, 0, len(k))
	for _, s := range k {
		parts = append(parts, Key{s}.String())
	}
	return parts
}

// replaceOrdered replaces the "from" key with the "to" key in the ordered
// key list, so MetaData.Keys and MetaData.Undecoded reflect the migration.
func replaceOrdered(p *parser, from, to Key) {
	fs := from.String()
	for i, k := range p.ordered {
		if k.String() == fs {
			p.ordered[i] = append(Key{}, to...)
		}
	}
}

// resolveMigrationField finds the struct field the key path "to" decodes to,
// using the same (case-insensitive) matching as the decoder. It returns
// false if any segment doesn't match a field. Map and interface destinations
// accept any key and always match.
func resolveMigrationField(t reflect.Type, to Key) (string, bool) {
	var name string
	for _, seg := range to {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Struct:
			fields := cachedTypeFields(t)
			var f *field
			for i := range fields {
				if fields[i].name == seg {
					f = &fields[i]
					break
				}
				if f == nil && strings.EqualFold(fields[i].name, seg) {
					f = &fields[i]
				}
			}
			if f == nil {
				return "", false
			}
			name = f.name
			t = f.typ
		case reflect.Slice, reflect.Array:
			t = t.Elem()
		case reflect.Map, reflect.Interface:
			return "", true
		default:
			return "", false
		}
	}
	return name, true
}
