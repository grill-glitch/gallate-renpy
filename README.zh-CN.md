# sirenhead-tool

**[English](./README.md) | [简体中文](./README.zh-CN.md)**

[![License: CC BY-SA 4.0](https://img.shields.io/badge/License-CC%20BY--SA%204.0-lightgrey.svg)](https://creativecommons.org/licenses/by-sa/4.0/)
[![GCWP: 1.0 Standard](https://img.shields.io/badge/GCWP-1.0%20Standard-blueviolet)](https://github.com/grill-glitch/gallate)
[![Python: ≥3.9](https://img.shields.io/badge/python-%E2%89%A53.9-blue)](https://www.python.org/)
[![Ren'Py: 7.x](https://img.shields.io/badge/Ren'Py-7.x-orange)](https://www.renpy.org/)

面向 Ren'Py 视觉小说 **"Siren Head Dating Sim"（警笛头约会模拟）**
的 [gallate](https://github.com/grill-glitch/gallate) CLI。把
`.rpy` 源文件里的对白和界面标签抽取成按源文件分组的 JSON 翻译单元，
翻译后再回填回去——保证字节级回环稳定、源文件漂移检测、原子
写入。

```text
符合规范：GCWP 1.0 Standard 级。

操作：
  -e / -i   抽取 / 回填
  -t        text 媒体（本 CLI 唯一暴露的媒体）

发现命令（见 docs/protocol/03）：
  manifest   CLI 身份（GCWP JSON 写到 stdout）
  features   CLI 能力（GCWP JSON 写到 stdout）
  validation 引擎专属校验规则（GCWP JSON）

子命令：
  init       新建 gallate.yaml 工程
```

## 为什么要写成 CLI 而不是脚本

`gallate`（以及 GCWP 线缆格式）允许同一个 Wrapper 驱动多个互不
相关的引擎 CLI，但任何一层都不需要耦合到其它层。本 CLI 同时实现
gallate 规范的两层：

- **Shell 层**——用户从 shell 用 `-e` / `-i` 调用。
- **协议层（GCWP）**——同一个二进制，由 Wrapper 通过 stdin 上的
  JSON Lines 驱动。

Wrapper 不需要知道 Ren'Py 存在；CLI 不需要知道 OmegaT 存在。

## 安装

### 从 PyPI（推荐，发布后）

```bash
pip install sirenhead-tool
sirenhead-tool --version
```

### 从源码（当前可用）

```bash
git clone https://github.com/grill-glitch/gallate-renpy
cd gallate-renpy
pip install -e .
sirenhead-tool --version
```

预期输出：

```text
sirenhead 0.1.0
protocol gcwp 1.0
engine renpy (7.x compatible)
```

### 直接使用源码树（不安装）

```bash
git clone https://github.com/grill-glitch/gallate-renpy
cd gallate-renpy
./sirenhead-tool --version   # 仓库根目录下的可执行脚本
```

唯一运行时依赖是 [PyYAML](https://pyyaml.org/) ≥ 6.0，大多数
Python 发行版自带。

## 快速上手

```bash
# 1. 在游戏目录旁边初始化工程
sirenhead-tool init /path/to/project \
    --input /path/to/game \
    --media text

# 2. 抽取翻译单元
sirenhead-tool -et /path/to/project/gallate.yaml

# 这会写入：
#   /path/to/project/text/units/script.json
#   /path/to/project/text/units/screens.json
#   /path/to/project/.meta.json

# 3. 翻译。每条记录形如：
#   {
#     "id": "script.rpy:L0042",
#     "source": "I decided to start my day with a walk in the woods.",
#     "target": "",                       # ← 在这里填译文
#     "state": "initial",
#     "source_context": {
#       "file": "script.rpy",
#       "line": 42,
#       "end_line": 42,
#       "snippet": "    c \"I decided to start my day...\""
#     },
#     "metadata": {
#       "source_file": "script.rpy",
#       "source_offset": 1107,
#       "source_length": 51,
#       "renpy_kind": "dialog",
#       "speaker": "c"
#     }
#   }

# 4. 回填
sirenhead-tool -it /path/to/project/gallate.yaml
```

CLI 把译文写回 `metadata.source_offset` /
`metadata.source_length` 记录的同一段字节区间。其余所有字节
（缩进、CRLF、Ren'Py 语法、注释）原样不动——文件 diff 每条
翻译对应恰好一行改动。

## 符合性

本 CLI 符合 gallate 仓库
[`docs/protocol/13-conformance.md`](https://github.com/grill-glitch/gallate/blob/main/docs/protocol/13-conformance.md)
定义的 **GCWP 1.0（Standard 级）**：

| 等级     | 能力                | 状态 |
| -------- | ------------------- | ---- |
| Basic    | `manifest`          | ✅   |
| Basic    | `features`          | ✅   |
| Basic    | `extract` 操作      | ✅   |
| Basic    | `inject` 操作       | ✅   |
| Basic    | 标准退出码          | ✅   |
| Standard | 事件流              | ✅   |
| Standard | 统计                | ✅   |
| Standard | 校验规则            | ✅   |
| Full     | 取消                | ❌ 未实现 |
| Full     | 状态流              | ❌ 未实现 |
| Full     | 全部校验类型        | 部分（regex + constraint） |

## 保证（由 `python -m tests.self_test` 验证）

1. **字节级回环**。`抽取 → 回填`（target 为空）会逐字节还
   原 `.rpy` 文件。
2. **最小 diff**。编辑 N 条单元产生恰好 N 行改动的 diff，
   其余行字节相同。
3. **源漂移检测**。若记录的偏移处源字符串与单元的 `source`
   不再一致，CLI 以退出码 8（Injection Failure）退出，且
   文件保持不动——杜绝静默错乱。
4. **可重复抽取**。连续两次 `抽取` 产生的 `.meta.json` 与
   单元文件相同（`generated_at` 除外）；位置派生的 ID 不会
   漂移。
5. **GCWP 握手 + 操作**。当 Wrapper 从 stdin 推 JSON 启动
   CLI 时，CLI 会响应 `protocol` ping，并按线缆格式跑
   `extract` / `inject` / `identify`。

干净克隆后可直接跑：

```bash
git clone https://github.com/grill-glitch/gallate-renpy
cd gallate-renpy
python3 -m tests.self_test
# ... ALL TESTS PASSED
```

针对真实 Siren Head Dating Sim 文件（更贴近现实，但需要本
地有游戏文件）：

```bash
SIRENHEAD_GAME_DIR=/path/to/SirenHeadDatingSim-1.0-pc/game \
    python3 -m tests.self_test
```

## 本 CLI 不做的事

- **图 / 音 / 视频抽取**。本游戏源文件里没有可本地化的图、
  音字符串。
- **build / repack**。Ren'Py 在运行时把 `.rpy` 编成 `.rpyc`，
  那是引擎的事。
- **Python 3 上的 AST 校验**。Ren'Py 7 自带的解析器是 Python 2
  代码（`import cPickle`）；在 Python 3 上正则就是权威。已经
  留了 `--engine.use-ast=true` 开关给装了 Python 2 的环境；
  其余情况是空操作。

## 退出码

| 码  | 含义                       | 参考 |
| --- | -------------------------- | ---- |
| 0   | 成功                       | shell-layer § 10.3 |
| 1   | 一般错误                   | shell-layer § 10.3 |
| 2   | CLI 用法错误               | shell-layer § 10.3 |
| 3   | `gallate.yaml` 配置错误    | shell-layer § 10.3 |
| 4   | 找不到输入                 | shell-layer § 10.3 |
| 5   | 不支持的操作               | shell-layer § 10.3 |
| 6   | 不支持的媒体               | shell-layer § 10.3 |
| 7   | 抽取失败                   | shell-layer § 10.3 |
| 8   | 回填失败                   | shell-layer § 10.3 |
| 9   | 输出失败                   | shell-layer § 10.3 |
| 10  | 脚本失败                   | shell-layer § 10.3 |

## 文件结构

```text
gallate-renpy/
├── sirenhead-tool             # 可执行入口脚本
├── sirenhead_tool/            # Python 包
│   ├── __init__.py
│   ├── __main__.py            # main() / 调度
│   ├── cli.py                 # Shell 层 argv 解析
│   ├── discovery.py           # manifest / features / 校验规则
│   ├── extract.py             # extract() / inject() 操作
│   ├── gcwp.py                # 协议层（stdin JSON 驱动）
│   ├── meta.py                # .meta.json 读取 / 原子写入
│   ├── renpy_extract.py       # Ren'Py 字符串抽取
│   └── py.typed               # PEP 561 标记
├── tests/
│   ├── __init__.py
│   ├── fixtures.py            # 自测用最小 Ren'Py 游戏
│   └── self_test.py           # 可跑的符合性测试
├── pyproject.toml             # PEP 517 构建 + 入口点
├── LICENSE                    # CC BY-SA 4.0
├── README.md / README.zh-CN.md
└── CHANGELOG.md
```

## 另见

- [gallate 规范](https://github.com/grill-glitch/gallate)——
  Shell 层（`docs/shell-layer/`）+ 协议层（`docs/protocol/`）
  + JSON Schema（`schema/`）。
- [Ren'Py 文档](https://www.renpy.org/doc/html/)——语言参考。

## 协议

[CC BY-SA 4.0](./LICENSE)——与 gallate 规范一致。本仓库不包含
"Siren Head Dating Sim" 的具体对白字符串——那些仍属原作者
所有。
