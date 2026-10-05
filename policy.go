package toml

import (
	"fmt"
	"strconv"
	"strings"
)

// UnknownKeyAction decides what happens with a TOML key that does not map to
// anything in the Go value passed to [Decoder.Decode] (an "unknown key").
type UnknownKeyAction int

const (
	// IgnoreUnknown leaves unknown keys alone. This is the default and matches
	// the historical behaviour: unknown keys are silently skipped and are only
	// observable through [MetaData.Undecoded].
	IgnoreUnknown UnknownKeyAction = iota

	// KeepUnknown stores unknown keys and their values in a separate sidecar
	// tree, available through [MetaData.Kept]. The values keep their TOML
	// types (int64, float64, string, bool, time.Time, map[string]any, []any).
	// Nothing is written to the decode target, and kept keys no longer show up
	// in [MetaData.Undecoded].
	KeepUnknown

	// RejectUnknown makes the decode fail with an [UnknownKeyError] listing
	// every unknown key the policy rejects. The decode target is not
	// delivered: the value passed to [Decoder.Decode] is left untouched.
	RejectUnknown
)

// PolicyRule applies an UnknownKeyAction to a structural key path.
//
// Path is matched as a prefix against the full path of an unknown key, so a
// rule with Path ["server"] applies to "server" itself and everything below
// it, such as "server.x". Each path element is one literal TOML key name; use
// [ParseKeyPath] to build a Path from a dotted string, which keeps quoted key
// names such as "a.b" as a single element instead of splitting them on the
// dot.
type PolicyRule struct {
	Path   Key
	Action UnknownKeyAction
}

// DecodePolicy decides, per structural key path, what happens with unknown
// keys: keys present in the TOML document that do not map to a struct field
// (including fields renamed with a toml struct tag) or map key in the decode
// target.
//
// Keys consumed by a custom decoder (a type implementing [Unmarshaler]) are
// never considered unknown, and neither are keys stored in a map target or
// behind a [Primitive].
//
// # Rule priority
//
// The action for an unknown key is resolved as follows:
//
//  1. The rule with the longest Path that is a prefix of the key wins
//     (the most specific rule applies).
//  2. If several rules have the same path length, the later rule in Rules
//     wins.
//  3. If no rule matches, Default is used. The zero value of Default is
//     [IgnoreUnknown], so a zero DecodePolicy reproduces the historical
//     behaviour.
//
// A rule with an empty Path matches every key and can be used as a catch-all
// that still loses to any more specific rule.
//
// # Example
//
// Keep unknown keys below [server] for later inspection, but reject any other
// unknown key:
//
//	dec := toml.NewDecoder(r)
//	dec.Policy(toml.DecodePolicy{
//		Default: toml.RejectUnknown,
//		Rules: []toml.PolicyRule{
//			{Path: toml.Key{"server"}, Action: toml.KeepUnknown},
//		},
//	})
//	md, err := dec.Decode(&conf)
//	// md.Kept() holds e.g. map[string]any{"server": map[string]any{"x": ...}}
type DecodePolicy struct {
	// Default is used for unknown keys not matched by any rule.
	Default UnknownKeyAction

	// Rules are matched by path prefix; see the documentation above for the
	// priority rules.
	Rules []PolicyRule
}

// Policy sets the decode policy for unknown keys. See [DecodePolicy] for
// details. The zero value keeps the historical behaviour of ignoring unknown
// keys.
func (dec *Decoder) Policy(p DecodePolicy) *Decoder {
	dec.policy = p
	return dec
}

// rejects reports whether any unknown key could be rejected by this policy.
func (p DecodePolicy) rejects() bool {
	if p.Default == RejectUnknown {
		return true
	}
	for _, r := range p.Rules {
		if r.Action == RejectUnknown {
			return true
		}
	}
	return false
}

// actionFor resolves the action for an unknown key with the given full path.
func (p DecodePolicy) actionFor(k Key) UnknownKeyAction {
	var (
		action  = p.Default
		bestLen = -1
	)
	for _, r := range p.Rules {
		if !keyHasPrefix(k, r.Path) {
			continue
		}
		// Longest path wins; on a tie the later rule wins.
		if len(r.Path) >= bestLen {
			bestLen = len(r.Path)
			action = r.Action
		}
	}
	return action
}

// keyHasPrefix reports whether prefix is a prefix of k. An empty prefix
// matches every key.
func keyHasPrefix(k, prefix Key) bool {
	if len(prefix) > len(k) {
		return false
	}
	for i := range prefix {
		if k[i] != prefix[i] {
			return false
		}
	}
	return true
}

// UnknownKeyError is returned by [Decoder.Decode] when a [DecodePolicy]
// rejects one or more unknown keys. Keys contains every rejected path, in the
// order the keys appear in the TOML document. When an entire table is
// unknown, only the table's own path is listed; the paths below it are
// implied.
//
// The decode target is not delivered when this error is returned.
type UnknownKeyError struct {
	Keys []Key
}

func (e UnknownKeyError) Error() string {
	ss := make([]string, len(e.Keys))
	for i, k := range e.Keys {
		ss[i] = k.String()
	}
	return fmt.Sprintf("toml: unknown keys rejected by decode policy: %s", strings.Join(ss, ", "))
}

// ParseKeyPath parses a dotted key path such as server."a.b" into a [Key]
// for use in [PolicyRule.Path].
//
// The syntax follows TOML key syntax: segments are separated by dots and may
// be bare keys, "basic strings", or 'literal strings'. A dot inside a quoted
// segment is part of the key name and is not split into separate path
// elements, so "a.b" parses to the single-element key ["a.b"], matching a
// TOML key literally named "a.b".
func ParseKeyPath(path string) (Key, error) {
	var key Key
	i, n := 0, len(path)
	for {
		for i < n && (path[i] == ' ' || path[i] == '\t') {
			i++
		}
		if i >= n {
			return nil, fmt.Errorf("toml: empty key path segment in %q", path)
		}
		switch c := path[i]; c {
		case '"':
			end := i + 1
			for end < n {
				if path[end] == '\\' {
					end += 2
					continue
				}
				if path[end] == '"' {
					break
				}
				end++
			}
			if end >= n {
				return nil, fmt.Errorf("toml: unterminated string in key path %q", path)
			}
			s, err := strconv.Unquote(path[i : end+1])
			if err != nil {
				return nil, fmt.Errorf("toml: invalid quoted key in path %q: %s", path, err)
			}
			key = append(key, s)
			i = end + 1
		case '\'':
			end := strings.IndexByte(path[i+1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("toml: unterminated literal string in key path %q", path)
			}
			key = append(key, path[i+1:i+1+end])
			i += end + 2
		default:
			start := i
			for i < n && isBareKeyChar(rune(path[i])) {
				i++
			}
			if i == start {
				return nil, fmt.Errorf("toml: invalid key path segment in %q", path)
			}
			key = append(key, path[start:i])
		}

		for i < n && (path[i] == ' ' || path[i] == '\t') {
			i++
		}
		if i >= n {
			return key, nil
		}
		if path[i] != '.' {
			return nil, fmt.Errorf("toml: expected '.' in key path %q", path)
		}
		i++
	}
}

// applyPolicy applies the decode policy to every key left undecoded after
// unification. Keys consumed by custom Unmarshaler implementations are marked
// decoded during unification, so they are never reported as unknown here.
//
// The action of every unknown key is resolved on its own, so a more specific
// rule still applies below a kept or rejected table. Keys below a kept or
// rejected key with the same action are implied by it and not listed or
// stored again.
func (md *MetaData) applyPolicy(p DecodePolicy) error {
	var rejected, kept []Key
	for _, k := range md.keys {
		if _, ok := md.decoded[k.String()]; ok {
			continue
		}
		switch p.actionFor(k) {
		case KeepUnknown:
			if !hasCoveredAncestor(k, kept) {
				kept = append(kept, k)
			}
		case RejectUnknown:
			if !hasCoveredAncestor(k, rejected) {
				rejected = append(rejected, k)
			}
		}
	}
	for _, k := range kept {
		md.markDecodedUnder(k)
		md.setKept(k)
	}
	if len(rejected) > 0 {
		return UnknownKeyError{Keys: rejected}
	}
	return nil
}

// hasCoveredAncestor reports whether any key in covered is a proper prefix
// of k.
func hasCoveredAncestor(k Key, covered []Key) bool {
	for _, c := range covered {
		if len(c) < len(k) && keyHasPrefix(k, c) {
			return true
		}
	}
	return false
}

// markDecodedUnder marks k and every key below it as decoded, so kept keys
// don't show up in Undecoded.
func (md *MetaData) markDecodedUnder(k Key) {
	for _, kk := range md.keys {
		if keyHasPrefix(kk, k) {
			md.decoded[kk.String()] = struct{}{}
		}
	}
}

// setKept copies the value at key path k from the parsed document into the
// sidecar tree, keeping the original TOML types.
func (md *MetaData) setKept(k Key) {
	v, ok := lookupKey(md.mapping, k)
	if !ok {
		return
	}
	if md.kept == nil {
		md.kept = make(map[string]any)
	}
	m := md.kept
	for _, seg := range k[:len(k)-1] {
		next, ok := m[seg].(map[string]any)
		if !ok {
			next = make(map[string]any)
			m[seg] = next
		}
		m = next
	}
	m[k[len(k)-1]] = v
}

// lookupKey finds the value at key path k in parsed TOML data. Arrays of
// tables are traversed by collecting the matching value from every element.
func lookupKey(data any, k Key) (any, bool) {
	if len(k) == 0 {
		return data, true
	}
	switch node := data.(type) {
	case map[string]any:
		v, ok := node[k[0]]
		if !ok {
			return nil, false
		}
		return lookupKey(v, k[1:])
	case []map[string]any:
		out := make([]any, 0, len(node))
		for _, elem := range node {
			if v, ok := lookupKey(elem, k); ok {
				out = append(out, v)
			}
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	}
	return nil, false
}
