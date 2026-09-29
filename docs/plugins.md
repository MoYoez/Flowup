# 本地插件

本地插件让你把已有的脚本或可执行文件接入 Flowup，不需要修改 Go 代码。插件处理一个动作，Flowup 负责传参、校验结果、保存状态和执行后续步骤。

## 安装、使用和卸载

先试仓库里的文字统计插件。需要 Python 3，终端里能执行 `python`。如果你的命令叫 `python3`，先修改示例清单的 `command`。

在你的项目根目录执行：

```sh
flowup plugin install examples/plugins/text-stats
flowup validate examples/plugins/text-stats/workflow.yaml
flowup run examples/plugins/text-stats/workflow.yaml --inputs examples/plugins/text-stats/inputs.json
```

安装路径可以是包含 `plugin.yaml` 的目录，也可以直接是清单文件。只支持本地路径。安装只检查并复制文件，不运行插件或安装依赖。需要的 Python、Node 等运行环境由你准备。

Windows 上的 `WindowsApps/python.exe` 可能只是应用执行别名，无法读取文件指纹。这时先执行 `python -c "import sys; print(sys.executable)"`，把清单 `command` 的第一个元素改成输出的真实解释器路径，再安装。路径包含空格时，仍把它作为数组里的单个字符串。

示例先统计文字，再等待确认。用 `flowup trace RUN_ID` 查看预览说明，然后执行终端给出的批准命令。批准后会再次调用插件，返回统计结果。这些命令都不需要 API Key。

```sh
flowup approve APPROVAL_ID
flowup plugin uninstall local.text_stats
```

插件安装在**执行命令时的当前目录**下：`.flowup/plugins/`。从该目录执行 `validate` 和 `run` 时，会加载工作流用到的已安装动作。不是全局安装，也不会根据工作流文件的位置自动向上查找项目。自定义 `--db` 只改变运行数据库的位置，不改变安装位置。

每个清单可以声明多个动作；安装输出会列出名字，卸载按名字进行。安装同名动作会更新新运行使用的版本，内置动作不能被替换。不需要插件的工作流不受其他已安装插件文件缺失的影响。

**卸载会移除新运行的调用入口，保留本地版本文件。** 已经开始的运行仍使用它保存的版本，所以卸载或更新后，待审批任务仍可继续。安装副本与原始文件分开；修改源文件后，重新 `install` 才会影响新运行。第一版没有自动清理历史版本的功能。

开发插件时，也可以直接使用源目录里的清单：

```sh
flowup validate workflow.yaml --plugins plugins.yaml
flowup run workflow.yaml --inputs inputs.json --plugins plugins.yaml
```

`--plugins` 会替代本次使用的已安装插件集合，不会写入安装记录。直接模式不会复制文件；修改原始文件会阻止旧运行继续，直到恢复原文件或开始新运行。

## 写一个插件

这是最小清单：

```yaml
version: 1
plugins:
  - name: local.text_stats
    version: "1.0.0"
    command: [python, stats.py]
    files: [stats.py]
    effect: read_only
    timeout: 10s
    input_schema:
      type: object
      properties:
        text: {type: string}
      required: [text]
    output_schema:
      type: object
```

`name` 至少包含一个点，例如 `local.text_stats`。工作流通过 `uses: local.text_stats` 调用。`command` 是程序和参数组成的数组，不是 Shell 命令字符串。Flowup 不会把工作流数据插进命令行，动态参数一律通过标准输入传递。

程序的工作目录是清单所在目录，安装后则是安装副本的目录。裸程序名如 `python` 从 PATH 查找；`./tool` 等相对可执行文件路径相对于清单。`files` 列出需要复制、检查的脚本、模块和静态配置，路径必须在清单目录内，且为普通文件。目录里的本地可执行文件也会复制。

脚本入口和本地依赖都应列入 `files`；没有自动扫描 import，也不会安装 requirements 或 npm 依赖。声明文件作为独立的绝对路径参数时，安装器会改为副本路径。不要把文件路径藏在 `--flag=/path` 或一段 Shell/Python 代码里，Flowup 不会解析这些内容。`.flowup-manifest.json` 是安装器保留文件名。

## 输入和输出协议

每次调用都会启动一个子进程。标准输入是一条 UTF-8 JSON 请求，读到 EOF 即可：

```json
{
  "protocol_version": 1,
  "run_id": "run_...",
  "step_id": "analyze",
  "attempt": 1,
  "input": {"text": "Hello"}
}
```

外部写动作还会收到 `idempotency_key`。插件如果调用支持幂等键的服务，可以把它传给服务；仅收到这个字段不代表请求天然不会重复。

成功时，进程退出码为 0，标准输出只写一个 JSON 对象：

```json
{"output": {"characters": 5, "lines": 1, "preview": "Hello"}}
```

Flowup 按 `output_schema` 检查 `output`，工作流通过 `steps.analyze.output` 引用它。失败时可以返回：

```json
{"error": {"message": "服务暂时不可用", "transient": true}}
```

`output` 和 `error` 必须二选一。未知响应字段、多条 JSON、无效 JSON 或非零退出码都作为失败处理。插件不能自己批准任务、改变步骤顺序或创建审批暂停；请在工作流里使用内置 `approval`。

标准输出上限为 1 MiB，标准错误上限为 64 KiB。超过任一上限会停止直接子进程。标准错误不显示、不保存，避免脚本意外打印凭据；可诊断的错误请通过 `error.message` 返回。声明的密钥字段及显式传入的环境变量值会从该错误消息中脱敏。插件输出仍会按原样保存，不要把密钥放进 `output`。

超时或取消会终止直接子进程，并限制等待输出管道结束的时间；不是整个进程树的沙箱或资源管理器。插件不应启动脱离管理的后台进程。

## 读写、凭据和恢复

- `effect: read_only`：声明动作只读。返回 `transient: true` 时，引擎最多尝试 3 次。
- `effect: external`：动作会写文件、发消息或产生其他外部变化。不自动重试；中断后无法确定操作是否发生时会停止，要求核实。`effect` 是作者声明，Flowup 无法验证任意代码确实只读。
- `timeout`：单次调用的最长时间，例如 `10s` 或 `2m`。
- `secret_paths`：允许使用 `${{ secrets.NAME }}` 的输入字段，例如 `[token, headers.*]`。凭据通过 JSON 输入传给插件，已保存的步骤输入会脱敏。包含密钥的输入校验失败时，不输出可能带原值的 schema 错误详情。
- `env`：额外继承的环境变量名，例如 `[MY_TOOL_CONFIG]`，只保存名称。默认仅继承 PATH、SystemRoot、WINDIR、TEMP、TMP、TMPDIR、PATHEXT 中存在的值，不继承整个父进程环境。

开始运行前，数据库会保存插件版本、参数、绝对路径，以及可执行文件和 `files` 中每个文件的 SHA-256。审批后继续或恢复时读取保存的配置，不依赖当前目录的安装记录；命令仍需指向同一数据库。

如果固定文件发生变化或丢失，会在批准前拒绝继续，审批仍保持待处理。恢复原文件后可以再试；`status`、`trace`、`reject` 不要求插件可用。终态运行也可以照常查询。运行中每次调用前还会检查指纹。

指纹不是完整的环境锁：未声明的依赖、解释器加载的包、环境变量值和外部数据仍可能变化。文件检查也不是不可篡改的执行隔离。

**本地插件是你信任的本地程序，具有当前操作系统账户可用的权限。** 最小环境、schema 和指纹用于约束调用、减少误用，不能限制插件访问其他文件或网络。Flowup 内置 HTTP 动作的地址限制不会自动约束插件自己发起的请求。

把 `.flowup/` 放进项目的 `.gitignore`，共享插件源码和清单即可。数据库会保存输入和输出，不应直接提交。安装或卸载请串行操作；第一版没有多进程安装协调。
