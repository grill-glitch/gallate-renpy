# sirenhead-tool

**[English](./README.md) | [简体中文](./README.zh-CN.md)**

[![License: CC BY-SA 4.0](https://img.shields.io/badge/License-CC%20BY--SA%204.0-lightgrey.svg)](https://creativecommons.org/licenses/by-sa/4.0/)
[![GCWP: 1.0 Standard](https://img.shields.io/badge/GCWP-1.0%20Standard-blueviolet)](https://github.com/grill-glitch/gallate)
[![Go: ≥1.22](https://img.shields.io/badge/go-%E2%89%A51.22-00ADD8)](https://go.dev/)
[![Ren'Py: 7.x](https://img.shields.io/badge/Ren'Py-7.x-orange)](https://www.renpy.org/)

一个用 **Go** 重写的 [gallate](https://github.com/grill-glitch/gallate) CLI，
面向 Ren'Py 视觉小说 **"Siren Head Dating Sim"**。把 `.rpy` 源码里的对白与
界面文本抽取成按源文件划分的 JSON 单元文件，翻译后再回填——回环逐字节一致、
带源漂移检测、原子写入。

```text
一致性：GCWP 1.0 Standard 层。

操作：
  -e / -i   extract / inject（抽取 / 回填）
  -t        text   标准媒体（基线，永远处理）
  -i        image  引擎扩展媒体（background / portrait / cg / ui）
  -a        audio  引擎扩展媒体（voice / bgm / sfx）
  -v        video  引擎扩展媒体（cutscene / opening / ending）

引擎扩展操作（Ren'Py）：
  unpack     <archive.rpa> <dir>   解包 RPA-3.0 归档
  repack     <dir> <archive.rpa>   将目录树打包为 .rpa 归档

发现（按 docs/protocol/03）：
  manifest   CLI 身份（stdout 上的 GCWP JSON）
  features   CLI 能力（stdout 上的 GCWP JSON）
  validation 引擎校验规则（stdout 上的 GCWP JSON）

子命令：
  init       创建一个 gallate.yaml 工程
```

## 为什么是 CLI 而不是一个脚本

`gallate`（以及 GCWP 线格式）让**一个** Wrapper 驱动**任意多**个互不相干的
引擎 CLI，而各层之间无需互相了解。本 CLI 同时实现规范的两层：

- **Shell 层** —— 在 shell 里用 `-e` / `-i` 调用。
- **协议层（GCWP）** —— 同一个二进制，由 Wrapper 通过 stdin/stdout 的
  JSON Lines 驱动。

Wrapper 不需要知道 Ren'Py 存在；CLI 也不需要知道 OmegaT 存在。

## 环境要求

- **Go ≥ 1.22**（仅构建期需要）。
- 唯一模块依赖：[`gopkg.in/yaml.v3`](https://gopkg.in/yaml.v3)（读 `gallate.yaml`）。
- 不需要 Python、虚拟环境或 pip。旧的 Python 实现在 `main` 分支上；本分支是
  Go 重写版。

## 安装

```bash
git clone https://github.com/grill-glitch/gallate-renpy
cd gallate-renpy
go build -o sirenhead-tool ./cmd/sirenhead-tool   # 或：make build
./sirenhead-tool --version
```

预期输出：

```text
sirenhead 0.4.0
protocol gcwp 1.0
engine renpy (7.x compatible)
```

`go install ./cmd/sirenhead-tool` 也可以，二进制会落在 `GOBIN`。

## 快速上手

```bash
# 1. 在游戏目录旁边初始化工程
./sirenhead-tool init /path/to/project \
    --input /path/to/game \
    --media text

# 2. 抽取翻译单元
./sirenhead-tool -et /path/to/project/gallate.yaml

# 产出：
#   /path/to/project/text/units/script.json
#   /path/to/project/text/units/screens.json
#   /path/to/project/.meta.json

# 3. 翻译。每条单元的形状是：
#   {
#     "id": "script.rpy:L0042",
#     "source": "I decided to start my day with a walk in the woods.",
#     "target": "",                       # ← 填这一项
#     "state": "initial",
#     "original_file": "script.rpy",
#     "source_context": {
#       "file": "script.rpy", "line": 42, "end_line": 43,
#       "snippet": "...上下文三行源码..."
#     },
#     "location": {"line": 42, "offset": 1107, "length": 51},
#     "placeholders": [],
#     "engine_path": "script.rpy",
#     "metadata": {
#       "source_file": "script.rpy",
#       "source_offset": 1107,
#       "source_length": 51,
#       "renpy_kind": "dialog",
#       "speaker": "c"
#     }
#   }

# 4. 回填
./sirenhead-tool -it /path/to/project/gallate.yaml
```

译文被写进 `metadata.source_offset` / `metadata.source_length` 记录的**同一段
字节区间**。其余字节（缩进、CRLF、Ren'Py 语法、注释）一个都不动——文件
diff 恰好每条改动一行。

这些键到底写不写，由 `gallate.yaml` 的 `text.metadata.*` 与
`text.lifecycle.written_on: never` 决定，详见 [gallate.yaml 支持](#gallateyaml-支持)。

## 打包归档（`.rpa`）

Ren'Py 把游戏素材（图、音、字体、`.rpyc` 编译脚本）打成 `.rpa` 归档发布。Ren'Py
运行时透明加载；本 CLI 的 `extract` / `inject` 需要磁盘文件，所以配两个子命令
把归档变目录树、再变回去：

```bash
# 1. 把归档展开成目录树。
./sirenhead-tool unpack /path/to/game/archive.rpa /tmp/unpacked

# 2. 对展开后的目录树（以及其中的译文副本）跑标准的 extract / inject / build。
./sirenhead-tool -et /path/to/project/gallate.yaml
./sirenhead-tool -it /path/to/project/gallate.yaml

# 3. 把（已翻译的）目录树重新打包成新归档。
./sirenhead-tool repack /tmp/unpacked /path/to/out.rpa
```

解包器会在目录树旁边写一个 `archive.manifest.json` 审计侧文件（再打包同一目录
时会自动排除它，避免把 CLI 自己的记账文件嵌进归档）。两个子命令都接受标准
`--ignore` 与 `--dry-run`；repack 还接受 `--engine.rpa-key=HEXHEXHEX` 覆盖默认
XOR 密钥（`42424242`，也是 Ren'Py 自己默认的）。

已在四份 DDLC 归档（`fonts` / `scripts` / `audio` / `images`）与 BAD END THEATER
的 `archive.rpa`（1901 条目）上验证。`unpack` → `repack` 的回环对每个文件都保持
逐字节一致。

GCWP 包装器用同一组操作：`{"operation":"unpack", ...}` 与 `{"operation":"repack", ...}`。

## 会抽取什么

Ren'Py 里玩家可见的字符串散落在几种不同位置，每种都要一条独立规则。以下
全部覆盖：

| 源码形状 | 例子 | 单元 kind |
| --- | --- | --- |
| 角色对白 | `c "Hello there."` | `dialog` |
| 旁白行 | `"Suddenly...Out of nowhere..."` | `narrator` |
| 静默对白节拍 | `"..."` | `narrator` |
| 多词 `menu:` 选项 | `"Run away":` | `menu_option` |
| 单词 `menu:` 选项 | `"Vanilla":` | `menu_option` |
| 玩家输入提示 | `$ n = renpy.input("What's your name?")` | `ui_prompt` |
| `_()` 包裹的界面文本 | `textbutton _("Back")` | `wrapped_text` |
| 屏幕文本 | `text "About"` | `screen_text` |

抽取器按优先级跑六遍（`wrapped_text` → `menu_option` → `ui_prompt` →
`dialog` → `screen_text` → `narrator`），并按**闭引号偏移**去重：同一段字面量
会被两遍以不同的起始偏移命中（`style_prefix "choice"` 整段命中，`"choice"`
单命中），按起始偏移去重会让回填把同一段字节改两次。散文启发式最后跑，且只
作用于裸缩进字符串——这也是单词菜单选项能活下来的原因（结构化的 `menu:` 那一遍
先抢下了它）。

**故意不抽**：图片/音频/视频文件名、Ren'Py 替换标记（`[name]`、
`[config.version]`）、`gui.rpy` / `options.rpy` 里的配置值、
`if name == "Jack"` 这类输入比较、键名（`Ctrl`、`Tab`）、日期格式串。

其中三种形状曾在"抽取成功"的假象下静默丢失（`_()` 界面文本——整个菜单不翻；
单词 `menu:` 选项——所有冰淇淋结局丢失；`renpy.input` 提示）。现在每种都有
自证用例，漏了就红。

## 保证（由 `go test ./...` 断言）

1. **回环逐字节一致。** 空译文时 `extract → inject` 还原出与原文件完全相同的
   `.rpy`。
2. **最小 diff。** 改 N 条 ⇒ 恰好 N 行变化，其余行逐字节相同，行数不变。
3. **源漂移检测。** 若单元记录偏移处的字节不再是它的 `source`，CLI 以退出码
   **8** 结束，并且**一个文件都不写**——连本来没问题的也不写。回填严格分两遍：
   第一遍校验全部闸门并在内存里重建，第二遍才落盘。
4. **抽取幂等。** 连跑两次 `extract`，`.meta.json`（除 `generated_at`）与
   位置派生的 id 完全一致。
5. **GCWP 握手 + 操作。** 由 Wrapper 通过 JSON Lines 驱动：`protocol` 握手、
   `identify`、`extract`、`inject`、`validate`，事件流含 `started` / `phase` /
   `progress` / `file` / `statistics` / `completed`，stdout 上没有人读的文本。
6. **原子写入。** 单元文件、媒体 sidecar、`.meta.json` 都是：同目录临时文件 →
   读回校验 → rename 替换。

### 拿真实游戏验证

```bash
go test ./...                                              # 仓库内 fixture
SIRENHEAD_GAME_DIR=/path/to/Game/game ./scripts/verify.sh   # 真实游戏
```

`scripts/verify.sh` 依次做 gofmt / vet / 测试 / 构建，然后用**构建出来的**二进制
跑真实游戏：抽取后游戏目录每个文件必须逐字节不变；空译文回填同样不能变；把源
字符串在 CLI 背后改掉后回填必须以退出码 8 中止且不写任何东西。

## gallate 一致性

### Shell 层

语法（见 [docs/shell-layer/02](https://github.com/grill-glitch/gallate/blob/main/docs/shell-layer/02-cli-grammar.md)）：

```text
sirenhead-tool [operation][media] ./gallate.yaml [options]
```

| 标志 | 行为 |
| --- | --- |
| `--output PATH` | 覆盖输出目标（相对路径按 Project Root 解析） |
| `--ignore PATTERN` | 追加忽略模式（可重复）。与 YAML 的 `ignore:` **合并**；支持 `.gitignore` 风格 glob（`*`、`**`、`?`、`dir/`） |
| `--dry-run` | 只规划不执行：解析配置/输入/媒体，报告计数，不写任何文件、不跑脚本 |
| `--force` | 仅 `init`：覆盖已存在的 `gallate.yaml` |
| `-v` / `-vv` / `-q` | 详细度。只影响表现层，**绝不**改变退出码 |
| `--engine.KEY=VALUE` | 引擎扩展选项（见下） |

退出码（[docs/shell-layer/10 § 10.3](https://github.com/grill-glitch/gallate/blob/main/docs/shell-layer/10-stdout-stderr.md)）：

| 码 | 含义 | 本 CLI 何时返回 |
| --- | --- | --- |
| 0 | 成功 | — |
| 1 | 一般错误 | 意外内部失败 |
| 2 | CLI 用法错误 | 参数非法、目标缺失/不可读、未知媒体字母 |
| 3 | 工程配置非法 | `gallate.yaml` 解析失败、缺 `input:`、子媒体过滤非法、`.meta.json` 版本高于本 CLI、`.meta.json` 指向的工程文件不存在 |
| 4 | 输入不存在 | `input:` 不是目录、工程记录的源资源缺失 |
| 5 | 不支持的操作 | — |
| 6 | 不支持的媒体 | 请求了已声明但引擎不支持的媒体 |
| 7 | 抽取失败 | 抽取期扫描/读取失败 |
| 8 | 回填失败 | 源漂移、编码不可写、记录区间内找不到引号对 |
| 9 | 输出失败 | 无法原子写出工程文件 |
| 10 | 脚本失败 | `scripts.pre` / `scripts.post` 返回非零 |

stdout 只承载操作结果（单行路径，因此 `result=$(sirenhead-tool -e ./gallate.yaml)`
可用）；进度、警告、错误一律走 stderr。

### `gallate.yaml` 支持

```yaml
input: ./game              # 必填
output: ./out              # 可选，默认输出目标
media: [text, image]       # 无媒体标志时的默认媒体列表
ignore: ["*.tmp", "cache/"]
scripts:
  pre:  ./scripts/pre.sh   # 操作前运行（cwd = Project Root）
  post: ./scripts/post.sh  # 操作后运行；失败退出 10

text:
  format: json             # json 是唯一标准单元格式
  layout: flat             # flat | mirror | single
  metadata:                # 五个规范字段的写出开关
    original_file: true
    source_context: true
    location: true
    placeholders: true
    engine_path: true
  hardcoded:
    context_lines: 3       # source_context.snippet 取上下文行数
    max_bytes: 4096        # 单条 snippet 上限（字符串自身那行保持完整）
  lifecycle:
    written_on: extract    # extract | never（never ⇒ 完全不写元数据）
  meta:                    # 五个字段的 JSON 键映射（纯声明）
    source_context: source_context
    location:       location

engine:
  text_encoding: utf-8
  image:
    includes: [background, portrait]   # 白名单
    excludes: [ui]                      # 减法；必须是 includes 的子集
  audio:
    excludes: [sfx]
```

引擎选项（`--engine.*`）：`in-place`（默认 `true`；为 `false` 时必须同时给
`--output`）、`max-length`（`max-target-length` 规则的软上限）、`use-ast`
（与 Ren'Py 自带解析器交叉校验——仅当宿主机存在 Python 2 且游戏带
`renpy/ast.py` 时才有效，否则是 no-op）、`image|audio|video.includes|excludes`
（子媒体过滤，覆盖 YAML 里的同名键）。

### 协议层（GCWP）

| 消息 / 事件 | 状态 |
| --- | --- |
| `protocol` 握手 | ✅（符合 schema：`protocol{name,version}` + 顶层 `version`，另带 `supported`） |
| `request` → `response` | ✅ |
| `started` / `phase` / `progress` / `file` / `warning` / `error` / `validation` / `statistics` / `completed` | ✅ |
| `identify` | ✅ |
| `cancel` 命令、`status` 查询 | ❌ 未实现（Full 层） |

协议层退出码遵循
[docs/protocol/02](https://github.com/grill-glitch/gallate/blob/main/docs/protocol/02-core-protocol.md)
（0 成功、1 操作失败、2 参数非法、3 配置非法、4 不支持的操作、5 校验失败、
6 已取消、7 协议错误、8 内部错误）——这是**另一张表**，Shell 层里的 6 表示
"不支持的媒体"。

声明的校验规则（`double-quote-balance`、`renpy-substitution-preserved`、
`max-target-length`）在回填时**真的会跑**，发现以 `validation` 事件发出并计入
统计。三条规则都是 `warning` / `info` 级别，所以发现本身不会改变退出码。

### 一致性层级

| 层 | 能力 | 状态 |
| --- | --- | --- |
| Basic | `manifest` | ✅ |
| Basic | `features` | ✅ |
| Basic | `extract` 操作 | ✅ |
| Basic | `inject` 操作 | ✅ |
| Basic | 标准退出码 | ✅ |
| Standard | 事件流（`started`、`phase`、`progress`、`file` …） | ✅ |
| Standard | 统计输出 | ✅ |
| Standard | 校验规则 | ✅ |
| Standard | image / audio / video（引擎扩展媒体） | ✅ |
| Full | 取消 | ❌ 未实现 |
| Full | 状态流 | ❌ 未实现 |
| Full | 全部校验规则类型 | 部分（regex + constraint；占位符改为逐单元自动检测，而非声明式规则） |

### 子媒体默认值

| 媒体 | 子媒体标识 | 分类依据 |
| --- | --- | --- |
| image | `background`、`portrait`、`cg`、`ui` | `gui/` 目录 → `ui`；文件名提示；脚本里的 `image` / `scene` / `show` 引用 |
| audio | `voice`、`bgm`、`sfx` | `voice` / `play music` / `play sound` 引用；目录与文件名提示 |
| video | `cutscene`、`opening`、`ending` | 文件名提示；`renpy.movie_cutscene` 引用 |

媒体是**文件替换**流程：抽取在每个资源旁写 JSON sidecar
（`image/<子媒体>/…`、`audio/<子媒体>/…`、`video/<子媒体>/…`）；回填把 sidecar
`target` 指向的文件覆盖到原位。`target` 为空即"保持原样"——这正是"不翻译则
逐字节一致"的由来。

## 0.4.0（Go 重写）改了什么

同一个 CLI、同一批单元 id、同一批偏移、同一批退出码、同一套线格式——外加下列
规范完备性修复。完整清单与兼容性说明见 [CHANGELOG.md](./CHANGELOG.md)。

- 单个静态二进制；不再需要 Python 运行时与 `pip install`。
- `--dry-run` 与 `--ignore` 真正生效（Python 版解析了却没用）。
- 补上 `progress` 与逐文件 `file` 事件（Standard 层要求）。
- `scripts:` 前置/后置脚本会被执行（失败退出 10）。
- 回填改为两遍，并优先以 `.meta.json` 作为"工程文件 ↔ 源资源"映射；没有记录
  位置时可由位置派生 id 或重跑抽取重新定位（单元文件本来就是可丢弃缓存）。
- 重新抽取不再抹掉已翻译内容：`target` / `state` / `context` / `notes` /
  `provenance` 会被继承；同一位置源文本变了则该条译文清空并标 `needs_review`。
- `.meta.json` 保留其它 CLI 的条目；`size` / `hash` 按规范描述**工程文件**。

## 本 CLI 不做的事

- **打包 / 重编译。** Ren'Py 自己按需从 `.rpy` 编译 `.rpyc`。
- **音频 / 视频重编码。** 替换文件原样拷入，由用户把文件放到相同路径
  （[docs/shell-layer/12 § 12.7](https://github.com/grill-glitch/gallate/blob/main/docs/shell-layer/12-file-structure.md)
  说视频应"只重封装不重编码"，那是上游的职责）。
- **普通宿主机上的 AST 校验。** Ren'Py 7 自带解析器是 Python 2 代码
  （`import cPickle`），所以 `--engine.use-ast=true` 在缺少 Python 2 或游戏未
  附带 `renpy/` 时是 no-op。正则抽取器本身是权威；AST 只作第二意见（补
  speaker）。
- **取消 / 状态查询**（GCWP Full 层）。
- **翻译记忆库。** 重跑抽取会重算派生字段，并按 id 保留未变位置的 `target`，
  但没有 TM 数据库——跨工程记忆请用 OmegaT sidecar 方案。
- **UTF-16 源文件。** 抽取器的字节偏移模型面向 UTF-8（带不带 BOM 都行）；
  UTF-16 的 `.rpy` 会带警告跳过，而不是给出对不上文件的偏移。

## 仓库结构

```text
gallate-renpy/
├── cmd/sirenhead-tool/main.go   # 入口（很薄）
├── internal/sirenhead/
│   ├── version.go       # CLI 身份——版本号唯一的来源
│   ├── exit.go          # 两张退出码表（Shell + GCWP）
│   ├── args.go          # Shell 层语法
│   ├── config.go        # gallate.yaml 加载与解析
│   ├── discovery.go     # manifest / features / validation
│   ├── renpy.go         # 六遍抽取
│   ├── units.go         # gallate.translation v1 文档与布局
│   ├── meta.go          # .meta.json（原子、区分归属）
│   ├── extract.go       # extract / inject 操作
│   ├── media.go         # image / audio / video 分类
│   ├── gcwp.go          # 协议层（stdin JSON Lines）
│   ├── scripts.go       # 前置 / 后置脚本
│   ├── reporter.go      # shell 走 stderr，GCWP 走事件
│   └── *_test.go        # 自证用例 + 仓库内 fixture
├── scripts/verify.sh    # 格式、vet、测试、真实游戏回环
├── Makefile
├── go.mod / go.sum
├── LICENSE              # CC BY-SA 4.0
├── README.md / README.zh-CN.md
└── CHANGELOG.md
```

## 另见

- [gallate 规范](https://github.com/grill-glitch/gallate) —— Shell 层
  （`docs/shell-layer/`）、协议层（`docs/protocol/`）、JSON Schema
  （`schema/`）。
- [Ren'Py 文档](https://www.renpy.org/doc/html/) —— 语言参考。

## 许可证

[CC BY-SA 4.0](./LICENSE)，与 gallate 规范一致。游戏内具体文本
（"Siren Head Dating Sim" 的对白）不属于本作品，版权归其各自作者。

## 作者

Samuel Flores
<5uniljdst@mozmail.com>

GitHub: [@grill-glitch](https://github.com/grill-glitch)
