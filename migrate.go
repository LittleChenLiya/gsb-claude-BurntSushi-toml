package toml

import (
	"fmt"
	"sort"
	"strings"
)

// Wildcard is a special [KeyMigration] path segment that matches any single
// key in a table, or every element of an array of tables. Wildcard matches
// are applied element by element, and may be referenced in the target path
// to keep the migrated value at the same level.
const Wildcard = "*"

// KeyMigration describes a single key migration applied before decoding:
// the value at the From path is moved to the To path, and is then decoded
// with the regular decoding rules.
//
// Paths are slices of key segments, so a literal key containing a dot (such
// as the quoted TOML key "a.b") is a single segment and is never confused
// with a nested path. The segment [Wildcard] matches any single key, or
// every element of an array of tables.
//
// A migration is a conflict, and reported as an error, when:
//   - the target key already exists (both the old and new key are present);
//   - the target path leads into a non-table value (e.g. below a scalar);
//   - migrations form a chain or cycle (a target of one migration is the
//     source of another).
//
// The input TOML document is never modified; migrations are applied to the
// parsed representation only.
type KeyMigration struct {
	From Key // Old key path.
	To   Key // New key path.
}

// MigrationRecord describes a single key migration that was applied during
// decoding. See [Decoder.DecodeWithMigrations].
type MigrationRecord struct {
	From     Key      // Source key path, with wildcards resolved.
	To       Key      // Target key path, with wildcards resolved.
	Field    string   // Name of the target field (last segment of To).
	Position Position // Position of the source key in the original input.
}

// DecodeWithMigrations decodes TOML data like [Decode], but applies the
// given key migrations to the parsed data before decoding it with the
// regular decoding rules. The applied migrations are reported in
// [MetaData.Migrations].
//
// See [KeyMigration] for the migration semantics and conflict rules.
func DecodeWithMigrations(data string, v any, migrations ...KeyMigration) (MetaData, error) {
	return NewDecoder(strings.NewReader(data)).DecodeWithMigrations(v, migrations...)
}

// wildValue is a single resolved Wildcard match: either a table key or an
// array-of-tables element index.
type wildValue struct {
	key     string
	index   int
	isIndex bool
}

// migrationMatch is a single resolved source key for a migration.
type migrationMatch struct {
	parent  map[string]any // Table holding the source key.
	key     string         // Source key in parent.
	value   any            // Value to move.
	wilds   []wildValue    // Resolved wildcards, in order of appearance.
	srcKey  Key            // Concrete source path (no array indices).
	display string         // Human-readable path, e.g. "servers[1].timeout".
	pos     Position       // Position of the source key in the input.
}

// applyMigrations applies all migrations to the parsed data, returning one
// record per applied migration.
func applyMigrations(p *parser, migrations []KeyMigration, input string) ([]MigrationRecord, error) {
	if err := validateMigrations(migrations); err != nil {
		return nil, err
	}

	var records []MigrationRecord
	for _, m := range migrations {
		var matches []migrationMatch
		if err := findMigrationMatches(p.mapping, m.From, nil, "", nil, &matches); err != nil {
			return nil, migrationError(p, input, m, migrationMatch{}, err.Error())
		}
		// Sort for deterministic behaviour when a Wildcard matches
		// multiple keys.
		sort.Slice(matches, func(i, j int) bool { return matches[i].display < matches[j].display })

		// Record source positions before any key metadata is rewritten.
		for i := range matches {
			matches[i].pos = p.keyInfo[matches[i].srcKey.String()].pos
		}

		rewritten := make(map[string]struct{})
		for _, match := range matches {
			toKey, _, err := setMigrationTarget(p.mapping, m, match)
			if err != nil {
				return nil, migrationError(p, input, m, match, err.Error())
			}
			delete(match.parent, match.key)

			// Move key metadata so positions, types, and key order keep
			// working for the migrated keys.
			if _, ok := rewritten[match.srcKey.String()]; !ok {
				rewritten[match.srcKey.String()] = struct{}{}
				rewriteKeyInfo(p, match.srcKey, toKey)
			}

			records = append(records, MigrationRecord{
				From:     match.srcKey,
				To:       toKey,
				Field:    toKey.last(),
				Position: match.pos.withCol(input),
			})
		}
	}
	return records, nil
}

func validateMigrations(migrations []KeyMigration) error {
	froms := make(map[string]int, len(migrations))
	tos := make(map[string]int, len(migrations))
	for i, m := range migrations {
		if len(m.From) == 0 || len(m.To) == 0 {
			return fmt.Errorf("toml: key migration %d: source and target paths must not be empty", i)
		}
		fromWilds, toWilds := countWildcards(m.From), countWildcards(m.To)
		if toWilds > fromWilds {
			return fmt.Errorf("toml: key migration %q -> %q: target has more wildcards than the source",
				m.From.String(), m.To.String())
		}
		if m.From.String() == m.To.String() {
			return fmt.Errorf("toml: key migration %q -> %q: source and target are identical",
				m.From.String(), m.To.String())
		}
		if _, ok := froms[m.From.String()]; ok {
			return fmt.Errorf("toml: key migration %q: duplicate migration source", m.From.String())
		}
		if _, ok := tos[m.To.String()]; ok {
			return fmt.Errorf("toml: key migration %q: duplicate migration target", m.To.String())
		}
		froms[m.From.String()] = i
		tos[m.To.String()] = i
	}
	for _, m := range migrations {
		if _, ok := froms[m.To.String()]; ok {
			return fmt.Errorf("toml: key migration %q -> %q: migration chains and cycles are not allowed: %q is both a target and a source",
				m.From.String(), m.To.String(), m.To.String())
		}
	}
	return nil
}

func countWildcards(k Key) int {
	var n int
	for _, seg := range k {
		if seg == Wildcard {
			n++
		}
	}
	return n
}

// findMigrationMatches finds all concrete source keys matching the From
// pattern. path is the concrete key path so far (no array indices), display
// the human-readable path (with array indices).
func findMigrationMatches(node any, segs []string, path Key, display string, wilds []wildValue, out *[]migrationMatch) error {
	seg := segs[0]
	last := len(segs) == 1

	switch node := node.(type) {
	case map[string]any:
		if seg == Wildcard {
			keys := make([]string, 0, len(node))
			for k := range node {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				w := append(wilds, wildValue{key: k})
				if last {
					*out = append(*out, migrationMatch{
						parent: node, key: k, value: node[k], wilds: w,
						srcKey: path.add(k), display: display + "." + k,
					})
				} else if err := findMigrationMatches(node[k], segs[1:], path.add(k), display+"."+k, w, out); err != nil {
					return err
				}
			}
			return nil
		}
		v, ok := node[seg]
		if !ok {
			return nil
		}
		if last {
			*out = append(*out, migrationMatch{
				parent: node, key: seg, value: v, wilds: wilds,
				srcKey: path.add(seg), display: display + "." + seg,
			})
			return nil
		}
		return findMigrationMatches(v, segs[1:], path.add(seg), display+"."+seg, wilds, out)

	case []map[string]any:
		if seg != Wildcard {
			return nil
		}
		if last {
			return fmt.Errorf("cannot migrate an entire array-of-tables element")
		}
		for i, elem := range node {
			w := append(wilds, wildValue{index: i, isIndex: true})
			disp := fmt.Sprintf("%s[%d]", display, i)
			if err := findMigrationMatches(elem, segs[1:], path, disp, w, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// setMigrationTarget moves the matched value to the migration target,
// creating intermediate tables as needed. It returns the concrete target
// path and its human-readable form.
func setMigrationTarget(root map[string]any, m KeyMigration, match migrationMatch) (Key, string, error) {
	// Resolve the concrete target path: Wildcard segments take the
	// corresponding concrete source segment, so wildcards always stay at
	// the same level.
	var concreteTo Key
	wi := 0
	for _, seg := range m.To {
		if seg == Wildcard {
			seg = m.From[wildcardIndex(m.From, wi)]
			wi++
		}
		concreteTo = append(concreteTo, seg)
	}

	var (
		node    any = root
		toKey   Key
		display string
	)
	wi = 0
	for i, seg := range m.To {
		last := i == len(m.To)-1
		if seg == Wildcard {
			w := match.wilds[wi]
			wi++
			if w.isIndex {
				arr, ok := node.([]map[string]any)
				if !ok {
					return nil, "", fmt.Errorf("target %q is not an array of tables", concreteTo.String())
				}
				if w.index >= len(arr) {
					return nil, "", fmt.Errorf("target %q has no element %d", concreteTo.String(), w.index)
				}
				node = arr[w.index]
				display += fmt.Sprintf("[%d]", w.index)
				toKey = append(toKey, concreteTo[i])
				continue
			}
			seg = w.key
		}

		tbl, ok := node.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("cannot migrate %q to %q: %q is not a table",
				displayPath(match.display), concreteTo.String(), displayPath(display))
		}
		if last {
			if _, exists := tbl[seg]; exists {
				return nil, "", fmt.Errorf("cannot migrate %q to %q: target key already exists",
					displayPath(match.display), displayPath(display+"."+seg))
			}
			tbl[seg] = match.value
		} else {
			child, exists := tbl[seg]
			if !exists {
				child = make(map[string]any)
				tbl[seg] = child
			}
			node = child
		}
		display += "." + seg
		toKey = append(toKey, seg)
	}
	return toKey, display, nil
}

func displayPath(d string) string { return strings.TrimPrefix(d, ".") }

// wildcardIndex returns the index of the n-th Wildcard segment in k.
func wildcardIndex(k Key, n int) int {
	for i, seg := range k {
		if seg == Wildcard {
			if n == 0 {
				return i
			}
			n--
		}
	}
	return -1
}

// rewriteKeyInfo rewrites key metadata (positions, types, and key order)
// from the old key prefix to the new one.
func rewriteKeyInfo(p *parser, from, to Key) {
	fs, ts := from.String(), to.String()
	for k, ki := range p.keyInfo {
		if k == fs || strings.HasPrefix(k, fs+".") {
			p.keyInfo[ts+k[len(fs):]] = ki
			delete(p.keyInfo, k)
		}
	}
	for i, k := range p.ordered {
		ks := k.String()
		if ks == fs || strings.HasPrefix(ks, fs+".") {
			nk := make(Key, 0, len(to)+len(k)-len(from))
			nk = append(nk, to...)
			nk = append(nk, k[len(from):]...)
			p.ordered[i] = nk
		}
	}
}

func migrationError(p *parser, input string, m KeyMigration, match migrationMatch, msg string) ParseError {
	src := match.srcKey
	if src == nil {
		src = m.From
	}
	pos := match.pos
	if pos.Line == 0 {
		pos = p.keyInfo[src.String()].pos
	}
	return ParseError{
		Message:  fmt.Sprintf("key migration %q -> %q: %s", m.From.String(), m.To.String(), msg),
		Position: pos.withCol(input),
		Line:     pos.Line,
		LastKey:  src.String(),
		input:    input,
	}
}
