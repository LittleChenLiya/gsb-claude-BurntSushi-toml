package toml

import (
	"fmt"
	"strings"
)

// UnknownKeyPolicy determines how unknown keys are handled during decoding.
//
// An unknown key is a TOML key that has no corresponding field in the target
// struct and is not consumed by a map, a [Primitive], or a custom decoder.
type UnknownKeyPolicy int

const (
	// UnknownKeyIgnore ignores unknown keys. This is the default behavior and
	// preserves the historical loose mapping.
	UnknownKeyIgnore UnknownKeyPolicy = iota
	// UnknownKeyReject rejects unknown keys by returning an error. When one or
	// more keys are rejected, the error lists every unknown path and the
	// decode is not delivered as successful.
	UnknownKeyReject
	// UnknownKeyRetain retains unknown keys in an independent bypass tree
	// (see [MetaData.Retained]) instead of discarding them. The retained
	// values keep their TOML types and are never written into the target
	// structure.
	UnknownKeyRetain
)

// String returns the name of the policy.
func (p UnknownKeyPolicy) String() string {
	switch p {
	case UnknownKeyIgnore:
		return "ignore"
	case UnknownKeyReject:
		return "reject"
	case UnknownKeyRetain:
		return "retain"
	}
	return fmt.Sprintf("UnknownKeyPolicy(%d)", int(p))
}

// UnknownKeyStrategy configures how unknown keys are handled during decoding.
//
// The zero value is a valid strategy that ignores all unknown keys (the
// default behavior). Use [NewUnknownKeyStrategy] to create a strategy with a
// non-default policy, and the Set/RejectAt/RetainAt/IgnoreAt methods to set
// per-path policies.
//
// # Strategy priority
//
// Per-path policies take precedence over the default policy. When multiple
// rules match a path, the most specific (longest) path wins. Paths are
// compared element by element, so a quoted key containing a literal dot
// (e.g. "a.b") is a single path element and is not split into "a" and "b".
//
// Rules are matched against the structural path of the unknown key, which
// follows field aliases and nested tables rather than the Go field names.
type UnknownKeyStrategy struct {
	defaultPolicy UnknownKeyPolicy
	rules         []unknownKeyRule
}

type unknownKeyRule struct {
	path   Key
	policy UnknownKeyPolicy
}

// NewUnknownKeyStrategy creates a strategy with the given default policy.
//
// The default policy applies to every unknown key that is not covered by a
// more specific per-path rule.
func NewUnknownKeyStrategy(defaultPolicy UnknownKeyPolicy) *UnknownKeyStrategy {
	return &UnknownKeyStrategy{defaultPolicy: defaultPolicy}
}

// Set sets the policy for the given path, returning the strategy for
// chaining.
//
// The path is a structural path, for example Key{"server"} for the [server]
// table or Key{"server", "tls"} for the [server.tls] table. A rule matches the
// path it is given and every path below it (a prefix match), with the most
// specific rule winning.
func (s *UnknownKeyStrategy) Set(path Key, policy UnknownKeyPolicy) *UnknownKeyStrategy {
	s.rules = append(s.rules, unknownKeyRule{path: path, policy: policy})
	return s
}

// RejectAt sets the policy at the given path to [UnknownKeyReject], returning
// the strategy for chaining.
func (s *UnknownKeyStrategy) RejectAt(path ...string) *UnknownKeyStrategy {
	return s.Set(Key(path), UnknownKeyReject)
}

// RetainAt sets the policy at the given path to [UnknownKeyRetain], returning
// the strategy for chaining.
func (s *UnknownKeyStrategy) RetainAt(path ...string) *UnknownKeyStrategy {
	return s.Set(Key(path), UnknownKeyRetain)
}

// IgnoreAt sets the policy at the given path to [UnknownKeyIgnore], returning
// the strategy for chaining.
func (s *UnknownKeyStrategy) IgnoreAt(path ...string) *UnknownKeyStrategy {
	return s.Set(Key(path), UnknownKeyIgnore)
}

// policyFor returns the policy that applies to the given path.
func (s *UnknownKeyStrategy) policyFor(path Key) UnknownKeyPolicy {
	if s == nil {
		return UnknownKeyIgnore
	}
	best := s.defaultPolicy
	bestLen := -1
	for _, rule := range s.rules {
		if rule.path.isPrefixOf(path) && len(rule.path) > bestLen {
			best = rule.policy
			bestLen = len(rule.path)
		}
	}
	return best
}

// isPrefixOf reports whether k is a prefix of other, comparing path elements.
// A quoted key containing a literal dot is a single element, so "a.b" does not
// match the prefix "a".
func (k Key) isPrefixOf(other Key) bool {
	if len(k) > len(other) {
		return false
	}
	for i := range k {
		if k[i] != other[i] {
			return false
		}
	}
	return true
}

// UnknownKeyError is returned when one or more unknown keys are rejected by
// an [UnknownKeyReject] policy.
//
// The error lists every rejected path in the order in which they appear in the
// TOML document. When this error is returned, the decode is not delivered as
// successful; the target structure should not be relied upon.
type UnknownKeyError struct {
	// Keys is the list of rejected paths.
	Keys []Key
}

func (e *UnknownKeyError) Error() string {
	var b strings.Builder
	b.WriteString("toml: unknown keys rejected: ")
	for i, k := range e.Keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k.String())
	}
	return b.String()
}
