package arraymerge_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/BurntSushi/toml/arraymerge"
)

// TestTomlEndToEnd exercises the full path: TOML documents are decoded into
// generic maps (the same shapes the merger works on), merged by declared
// identity field, and the result is checked.
func TestTomlEndToEnd(t *testing.T) {
	baselineTOML := `
[[services]]
name = "a"
port = 80

[[services]]
name = "b"
port = 8080
`
	overrideTOML := `
[[services]]
name = "b"
port = 9090

[[services]]
name = "c"
port = 3000
`
	var rawBase, rawOver map[string]interface{}
	if _, err := toml.Decode(baselineTOML, &rawBase); err != nil {
		t.Fatal(err)
	}
	if _, err := toml.Decode(overrideTOML, &rawOver); err != nil {
		t.Fatal(err)
	}
	// toml produces typed slices (e.g. []map[string]interface{}); normalize
	// them to the uniform map[string]interface{} / []interface{} shape.
	baseline := arraymerge.NormalizeConfig(rawBase)
	override := arraymerge.NormalizeConfig(rawOver)

	m := arraymerge.New().WithIdentity("services", "name")

	// Merge: override b's port and append c; order stays a,b,c.
	merged, err := m.Merge(baseline, override, nil)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	var bPort int64
	for _, raw := range merged["services"].([]interface{}) {
		svc := raw.(map[string]interface{})
		order = append(order, svc["name"].(string))
		if svc["name"] == "b" {
			bPort = svc["port"].(int64)
		}
	}
	if !reflect.DeepEqual(order, []string{"a", "b", "c"}) {
		t.Fatalf("order = %v", order)
	}
	if bPort != 9090 {
		t.Fatalf("b port = %d", bPort)
	}

	// Delete a explicitly: only b,c remain.
	afterDelete, err := m.Merge(baseline, override,
		map[string][]string{"services": {"a"}})
	if err != nil {
		t.Fatal(err)
	}
	order = nil
	for _, raw := range afterDelete["services"].([]interface{}) {
		order = append(order, raw.(map[string]interface{})["name"].(string))
	}
	if !reflect.DeepEqual(order, []string{"b", "c"}) {
		t.Fatalf("after delete order = %v", order)
	}

	// Unknown delete ID is a hard error and yields no result.
	if result, err := m.Merge(baseline, override,
		map[string][]string{"services": {"zzz"}}); err == nil || result != nil {
		t.Fatalf("expected explicit error for unknown delete ID, got result=%v err=%v", result, err)
	}
}

// ExampleMerger shows the complete documented usage flow.
func ExampleMerger() {
	baseline := map[string]interface{}{
		"services": []interface{}{
			map[string]interface{}{"name": "a", "port": int64(80)},
			map[string]interface{}{"name": "b", "port": int64(8080)},
		},
	}
	override := map[string]interface{}{
		"services": []interface{}{
			map[string]interface{}{"name": "b", "port": int64(9090)},
			map[string]interface{}{"name": "c", "port": int64(3000)},
		},
	}
	merged, err := arraymerge.New().
		WithIdentity("services", "name").
		Merge(baseline, override, map[string][]string{"services": {"a"}})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	for _, raw := range merged["services"].([]interface{}) {
		svc := raw.(map[string]interface{})
		fmt.Printf("%s:%d ", svc["name"], svc["port"])
	}
	// Output: b:9090 c:3000
}
