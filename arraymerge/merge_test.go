package arraymerge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func service(id string, kvs ...interface{}) map[string]interface{} {
	m := map[string]interface{}{"name": id}
	for i := 0; i+1 < len(kvs); i += 2 {
		m[kvs[i].(string)] = kvs[i+1]
	}
	return m
}

func names(items []interface{}) []string {
	out := make([]string, len(items))
	for i, raw := range items {
		out[i] = raw.(map[string]interface{})["name"].(string)
	}
	return out
}

func baselineServices() map[string]interface{} {
	return map[string]interface{}{
		"services": []interface{}{
			service("a", "port", int64(80)),
			service("b", "port", int64(8080)),
		},
	}
}

func overrideServices() map[string]interface{} {
	return map[string]interface{}{
		"services": []interface{}{
			service("b", "port", int64(9090)),
			service("c", "port", int64(3000)),
		},
	}
}

// 验收 1：基线 a,b；覆盖 b 端口并新增 c 后顺序必须为 a,b,c。
func TestAcceptanceMergeOrderAndFieldOverride(t *testing.T) {
	m := New().WithIdentity("services", "name")
	got, err := m.Merge(baselineServices(), overrideServices(), nil)
	if err != nil {
		t.Fatalf("expected merge to succeed, got error: %v", err)
	}
	items := got["services"].([]interface{})
	if got, want := names(items), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	b := items[1].(map[string]interface{})
	if b["port"] != int64(9090) {
		t.Fatalf("b port = %v, want 9090 (override applied)", b["port"])
	}
	a := items[0].(map[string]interface{})
	if a["port"] != int64(80) {
		t.Fatalf("a port = %v, want 80 (baseline retained)", a["port"])
	}
	c := items[2].(map[string]interface{})
	if c["port"] != int64(3000) {
		t.Fatalf("c port = %v, want 3000 (new item appended)", c["port"])
	}
}

// 验收 2：删除 a 后只余 b,c（顺序仍为基线优先，新项在后）。
func TestAcceptanceDeleteExplicit(t *testing.T) {
	m := New().WithIdentity("services", "name")
	got, err := m.Merge(baselineServices(), overrideServices(),
		map[string][]string{"services": {"a"}})
	if err != nil {
		t.Fatalf("expected delete to succeed, got error: %v", err)
	}
	items := got["services"].([]interface{})
	if got, want := names(items), []string{"b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order after delete = %v, want %v", got, want)
	}
}

// 验收 3：一侧出现重复身份 b 时不交付结果，并在错误中给出所在侧与位置。
func TestAcceptanceDuplicateRejectsResult(t *testing.T) {
	dup := map[string]interface{}{
		"services": []interface{}{
			service("b", "port", int64(9090)),
			service("b", "port", int64(3000)),
		},
	}
	m := New().WithIdentity("services", "name")
	got, err := m.Merge(baselineServices(), dup, nil)
	if err == nil {
		t.Fatal("expected duplicate identity error, got nil")
	}
	if got != nil {
		t.Fatalf("duplicate identity must not deliver a result, got %#v", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, `"b"`) || !strings.Contains(msg, "items [0 1]") {
		t.Fatalf("error must name identity and positions, got: %v", msg)
	}
	if !strings.Contains(msg, "services") {
		t.Fatalf("error must locate the array table, got: %v", msg)
	}
}

// 验收 4：删除清单中的未知 ID 必须作为明确错误，且不返回结果。
func TestDeleteUnknownIDIsExplicitError(t *testing.T) {
	m := New().WithIdentity("services", "name")
	got, err := m.Merge(baselineServices(), overrideServices(),
		map[string][]string{"services": {"nope"}})
	if err == nil {
		t.Fatal("expected explicit error for unknown delete ID, got nil")
	}
	if got != nil {
		t.Fatalf("unknown delete ID must not deliver a result, got %#v", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, `"nope"`) || !strings.Contains(msg, "not found") {
		t.Fatalf("error must clearly name the unknown ID, got: %v", msg)
	}
}

// 删除清单引用了未声明身份字段的表，也是明确错误。
func TestDeleteListForUnknownTable(t *testing.T) {
	m := New().WithIdentity("services", "name")
	_, err := m.Merge(baselineServices(), overrideServices(),
		map[string][]string{"ghosts": {"x"}})
	if err == nil {
		t.Fatal("expected error for delete list of unknown table")
	}
	if !strings.Contains(err.Error(), "unknown table") {
		t.Fatalf("error must explain the table is unknown, got: %v", err)
	}
}

// 删除只出现在覆盖侧的新项也应成功（ID 在双方任一侧存在即可）。
func TestDeleteNewOverrideItem(t *testing.T) {
	m := New().WithIdentity("services", "name")
	got, err := m.Merge(baselineServices(), overrideServices(),
		map[string][]string{"services": {"c"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := names(got["services"].([]interface{})), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after deleting new override c, order = %v, want %v", got, want)
	}
}

// 缺失身份字段的项必须报错并指出侧别和位置。
func TestMissingIdentityFieldReported(t *testing.T) {
	base := map[string]interface{}{
		"services": []interface{}{
			map[string]interface{}{"port": int64(80)},
		},
	}
	over := map[string]interface{}{
		"services": []interface{}{
			service("b"),
		},
	}
	m := New().WithIdentity("services", "name")
	_, err := m.Merge(base, over, nil)
	if err == nil {
		t.Fatal("expected missing identity error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "baseline item 0") || !strings.Contains(msg, `missing identity field "name"`) {
		t.Fatalf("error must report side and position, got: %v", msg)
	}
}

// 双方都有重复身份时，所有重复位置都应一并报告。
func TestDuplicatesReportedOnBothSides(t *testing.T) {
	base := map[string]interface{}{
		"services": []interface{}{
			service("b"), service("b"),
		},
	}
	over := map[string]interface{}{
		"services": []interface{}{
			service("c"), service("c"),
		},
	}
	m := New().WithIdentity("services", "name")
	_, err := m.Merge(base, over, nil)
	merr, ok := err.(MultiError)
	if !ok {
		t.Fatalf("expected MultiError, got %T: %v", err, err)
	}
	var sawBaseDup, sawOverDup bool
	for _, e := range merr {
		if strings.Contains(e.Detail, "baseline") {
			sawBaseDup = true
		}
		if strings.Contains(e.Detail, "override") {
			sawOverDup = true
		}
	}
	if !sawBaseDup || !sawOverDup {
		t.Fatalf("expected duplicates reported for both sides, got: %v", err)
	}
}

// 空覆盖数组不是删除：普通数组保持基线不变；身份数组空覆盖也保持基线。
func TestEmptyOverrideIsNotDeletion(t *testing.T) {
	base := map[string]interface{}{
		"tags":     []interface{}{"x", "y"},
		"services": []interface{}{service("a", "port", int64(80))},
	}
	over := map[string]interface{}{
		"tags":     []interface{}{},
		"services": []interface{}{},
	}
	m := New().WithIdentity("services", "name")
	got, err := m.Merge(base, over, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []interface{}{"x", "y"}; !reflect.DeepEqual(got["tags"], want) {
		t.Fatalf("empty ordinary override must not erase baseline, got %v", got["tags"])
	}
	if want := []string{"a"}; !reflect.DeepEqual(names(got["services"].([]interface{})), want) {
		t.Fatalf("empty identity override must not erase baseline, got %v", got["services"])
	}
}

// 普通数组整体替换为覆盖值。
func TestOrdinaryArrayReplacedWholesale(t *testing.T) {
	base := map[string]interface{}{"tags": []interface{}{"x", "y", "z"}}
	over := map[string]interface{}{"tags": []interface{}{"p", "q"}}
	m := New()
	got, err := m.Merge(base, over, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []interface{}{"p", "q"}; !reflect.DeepEqual(got["tags"], want) {
		t.Fatalf("ordinary array should be replaced wholesale, got %v", got["tags"])
	}
}

// 同身份项递归合并：map 按键合并，普通数组整体替换。
func TestRecursiveMergeWithinIdentity(t *testing.T) {
	base := map[string]interface{}{
		"services": []interface{}{
			service("a", "port", int64(80), "tags", []interface{}{"old"}, "meta",
				map[string]interface{}{"region": "cn", "keep": true}),
		},
	}
	over := map[string]interface{}{
		"services": []interface{}{
			service("a", "tags", []interface{}{"new"}, "meta",
				map[string]interface{}{"region": "us"}),
		},
	}
	m := New().WithIdentity("services", "name")
	got, err := m.Merge(base, over, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	a := got["services"].([]interface{})[0].(map[string]interface{})
	if a["port"] != int64(80) {
		t.Fatalf("baseline-only key should survive, got %v", a["port"])
	}
	if want := []interface{}{"new"}; !reflect.DeepEqual(a["tags"], want) {
		t.Fatalf("nested ordinary array should be replaced wholesale, got %v", a["tags"])
	}
	meta := a["meta"].(map[string]interface{})
	if meta["region"] != "us" || meta["keep"] != true {
		t.Fatalf("nested map should merge key-by-key, got %#v", meta)
	}
}

// 指针隔离：合并后修改结果不影响原始基线与覆盖，原始 map/slice 不被改动。
func TestPointerIsolation(t *testing.T) {
	base := baselineServices()
	over := overrideServices()
	baseJSON, _ := json.Marshal(base)
	overJSON, _ := json.Marshal(over)

	m := New().WithIdentity("services", "name")
	got, err := m.Merge(base, over, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Mutate the result aggressively.
	items := got["services"].([]interface{})
	items[0].(map[string]interface{})["port"] = int64(1)
	items[0].(map[string]interface{})["mutated"] = true
	got["extra"] = int64(42)

	if cur := string(baseJSON); string(mustJSON(base)) != cur {
		t.Fatalf("baseline was modified by merge/mutation:\n%s\nwant %s", mustJSON(base), cur)
	}
	if cur := string(overJSON); string(mustJSON(over)) != cur {
		t.Fatalf("override was modified by merge/mutation:\n%s\nwant %s", mustJSON(over), cur)
	}
	// Also confirm result slices are distinct allocations.
	if &items[0] == &base["services"].([]interface{})[0] {
		t.Fatal("result shares slice storage with baseline")
	}
}

func mustJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// 嵌套身份数组同样按身份递归合并。
func TestNestedIdentityArray(t *testing.T) {
	base := map[string]interface{}{
		"top": map[string]interface{}{
			"services": []interface{}{
				service("a", "port", int64(80)),
				service("b", "port", int64(8080)),
			},
		},
	}
	over := map[string]interface{}{
		"top": map[string]interface{}{
			"services": []interface{}{
				service("b", "port", int64(9090)),
				service("c", "port", int64(3000)),
			},
		},
	}
	m := New().WithIdentity("services", "name")
	got, err := m.Merge(base, over, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	items := got["top"].(map[string]interface{})["services"].([]interface{})
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(names(items), want) {
		t.Fatalf("nested identity order = %v, want %v", names(items), want)
	}
}

// 纯新增的覆盖键（基线没有）直接加入；纯基线键保留。
func TestAddedAndUnchangedKeys(t *testing.T) {
	base := map[string]interface{}{"old": "keep"}
	over := map[string]interface{}{"new": "add"}
	got, err := New().Merge(base, over, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["old"] != "keep" || got["new"] != "add" {
		t.Fatalf("expected both keys, got %#v", got)
	}
}

// 类型冲突（基线是表，覆盖是数组）直接报错，不交付结果。
func TestKindMismatch(t *testing.T) {
	base := map[string]interface{}{"x": map[string]interface{}{}}
	over := map[string]interface{}{"x": []interface{}{}}
	got, err := New().Merge(base, over, nil)
	if err == nil {
		t.Fatal("expected kind mismatch error")
	}
	if got != nil {
		t.Fatalf("kind mismatch must not deliver a result, got %#v", got)
	}
}

// Normalize 应把解码器的类型化切片转成统一形状，且不共享底层存储。
func TestNormalizeTypedSlices(t *testing.T) {
	in := map[string]interface{}{
		"services": []map[string]interface{}{
			{"name": "a", "port": int64(80)},
		},
		"ports": []int64{1, 2},
	}
	got := NormalizeConfig(in)
	items, ok := got["services"].([]interface{})
	if !ok {
		t.Fatalf("services normalized to %T, want []interface{}", got["services"])
	}
	if items[0].(map[string]interface{})["name"] != "a" {
		t.Fatal("normalized table content wrong")
	}
	if ports, ok := got["ports"].([]interface{}); !ok || ports[1] != int64(2) {
		t.Fatalf("ports normalized wrong: %#v", got["ports"])
	}
	// Original untouched, no shared slice header.
	if _, typed := in["services"].([]map[string]interface{}); !typed {
		t.Fatal("Normalize must not modify the input")
	}
}
