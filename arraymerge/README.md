# arraymerge — 配置数组表合并器

`arraymerge` 把“覆盖配置”（override）按**声明的身份字段**合并到“基线配置”
（baseline），面向 TOML/JSON 一类解码得到的通用 Go 值
（`map[string]interface{}` / `[]interface{}`，叶子为 `string`/`int64`/
`float64`/`bool`/`time.Time` 等）。

## 用法

```go
m := arraymerge.New().
    WithIdentity("services", "name") // services 数组表以 name 作为身份

merged, err := m.Merge(baseline, override,
    map[string][]string{"services": {"a"}}) // 独立的显式删除 ID 清单
```

若数据来自 `toml.Decode` 到 `map[string]interface{}`，数组表会被解码成
`[]map[string]interface{}`、原始值数组会被解码成 `[]int64` 等类型化切片，
先用 `arraymerge.NormalizeConfig`（或 `Normalize`）规整为统一形状再合并。

## 合并规则

| 结构 | 规则 |
| --- | --- |
| 身份数组表（`WithIdentity` 声明） | 按身份字段匹配：仅基线有的项**保持原顺序**；身份相同的项**递归合并**；仅覆盖有的项**追加到末尾**（按覆盖顺序）。 |
| 身份项内部（递归） | `map` 按键深度合并；普通数组整体替换；嵌套的身份数组表遵循同样规则。 |
| 普通数组 | 用覆盖值**整体替换**。 |
| 空覆盖数组 | **不解释为删除**：覆盖数组为空时保留基线数组不变（普通数组与身份数组皆然）。删除只能通过独立的删除 ID 清单。 |
| 删除清单 | `map[表名][]ID`，只对声明了身份字段的数组表生效；删除在合并之后应用。 |

## 校验（全部通过才返回新配置）

进入合并前先做一次完整校验，问题全部收集后一并返回（单个为 `*MergeError`，
多个为 `MultiError`），任何一项失败都返回 `nil` 结果、**不交付半成品**：

- 重复身份：报错并给出**所在侧（baseline/override）和项位置**（如
  `items [0 1]`）以及所在数组表路径；
- 缺失/空/非字符串身份字段：同样报出侧别与项位置；
- 删除清单中的**未知 ID**（基线、覆盖双方都找不到）作为**明确错误**处理；
- 删除清单引用了未声明身份字段的表、空 ID、表/数组类型冲突也均报错。

## 指针隔离

合并先对 baseline/override 做深拷贝，再在私有副本上合并；返回的配置中所有
`map`/`slice` 都与输入不共享存储。**原 map 和 slice 不会被修改**，合并后任意
改动结果也不会回灌到输入。

## 验收场景

- 基线 `services` 为 `a,b`，覆盖中修改 `b` 的端口并新增 `c` ⇒ 结果顺序
  `a,b,c`，`b` 端口为覆盖值，`a` 保持基线值；
- 删除清单删除 `a` ⇒ 只剩 `b,c`；
- 覆盖侧出现重复身份 `b` ⇒ 返回错误、不交付结果，错误给出双方位置。

## 运行测试

所有工具缓存定向到 D 盘，仅运行本包的小范围确定性测试：

```bash
TEMP=D:\Codex\tmp TMP=D:\Codex\tmp \
GOCACHE=D:\Codex\.cache\go-build GOMODCACHE=D:\Codex\go\pkg\mod \
go test ./arraymerge/ -count=1 -v
```

竞态检测：

```bash
go test ./arraymerge/ -race -count=1
```

静态检查与格式：

```bash
go vet ./arraymerge/
gofmt -l arraymerge/   # 无输出即合格
```
