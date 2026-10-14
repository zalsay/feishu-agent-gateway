# 飞书 AI Agent 集合网关（Go）

一个**飞书应用机器人**、一个私聊入口，按文本别名路由到 Codex CLI 或 Hermes API。`@codex` / `@hermes` 是机器人解释的文字，**不是飞书原生的第二个 @ 身份**。

## 功能

| 发给机器人 | 行为 |
|---|---|
| `/agents` | 列出后端 |
| `/use codex`、`/use hermes` | 设置这个私聊的默认后端 |
| `@codex 帮我分析这个仓库` | 仅这一条指定 Codex，不改变默认 |
| `@hermes 解释这段代码` | 仅这一条指定 Hermes |
| `/reset` | 清除当前默认后端的续聊上下文 |
| `/help` | 查看用法 |

选择默认后，普通文本直接续聊。不同飞书用户、不同后端的上下文分别保存。只接收**被授权用户的机器人私聊文本**；群聊和未授权消息静默忽略，图片/文件回复不支持说明。

## 飞书侧准备

1. 在[飞书开放平台](https://open.feishu.cn/)创建**企业自建应用**并启用**机器人**；不是群自定义 Webhook 机器人。设置应用可用范围，并由管理员按企业流程发布/授权。
2. 订阅 `im.message.receive_v1`（接收消息）事件，选择**长连接**方式；启用发送机器人消息权限，例如 `im:message:send_as_bot`。根据开发者后台的权限提示补充接收消息所需权限并发布版本。
3. 将 App ID 和 App Secret 配到服务端；将实际发送者 `UserID`（通常为 open_id）加入 `FEISHU_ALLOWED_USER_IDS`。先从事件/开发者调试工具确认该 ID，**不要把任意用户开放为默认允许**。

使用飞书官方 Go SDK 的 WebSocket EventDispatcher 直接接收事件、Channel API 发送回复；不经过 Channel 内部异步批处理队列，避免把相邻命令拼在一起，并使停机时可等待已接收的回调任务。无需对公网暴露事件回调地址。

## 后端准备

- **Codex**：服务进程可找到 `codex` CLI 并完成认证；`CODEX_REPO` 指向固定的、受信任的 Git 仓库，`GATEWAY_CODEX_HOME` 指向**独立且仅服务账户可访问**的 Codex home。先在该 home 认证 Codex，例如 `CODEX_HOME=/path/to/private/codex-home codex login`。新会话使用 `codex exec --json --ignore-user-config --ignore-rules --sandbox read-only`；续聊前经 app-server `thread/read` 检查旧会话是否存在，随后强制 `sandbox_mode="read-only"`。飞书消息**不能**选择仓库、启动参数或提升权限；此版本不开放写仓库功能，但 Codex 沙箱并非操作系统级安全边界；不要在专用 Codex home 中安装高权限 MCP/插件。建议使用独立低权限服务账户和独立只读仓库/容器，避免把个人认证材料放在共享主机上。
- **Hermes**：在其部署中启用 API server，设置 `API_SERVER_ENABLED=true`、`API_SERVER_KEY`，运行 `hermes gateway`。网关调用 `POST /v1/responses`、`store: true`，续聊时先 `GET /v1/responses/{id}` 检查旧响应，按用户维护 `previous_response_id`。同时发送基于飞书会话和 Agent 的稳定哈希 `X-Hermes-Session-Key`，用于 Hermes 支持的长期记忆隔离；某些 Hermes memory 插件仍可能忽略该键，敏感多用户环境须先验证插件行为或为用户分别部署 profile。远端 Hermes 只允许 HTTPS，本机 loopback 可使用 HTTP。请在 Hermes 侧把工具权限配置为与你的安全策略一致：**本版没有把 Hermes 的工具审批转接到飞书**，不要给它不受约束的生产执行权限。

## 运行

要求 Go 1.25+；网关、Codex CLI 和 Hermes API 能互相连通。

```sh
cp .env.example .env
# 修改 .env 中的凭证、允许用户、Hermes 地址、固定仓库和专用 Codex home；不要提交该文件。
mkdir -p /absolute/path/to/private/codex-home
# 使用该路径完成 Codex 认证：CODEX_HOME=/absolute/path/to/private/codex-home codex login
set -a; . ./.env; set +a
go run ./cmd/gateway
```

生产环境建议由进程管理器注入环境变量，并将 `GATEWAY_STATE_PATH` 指向仅服务账户可写的持久目录；**只运行一个实例**。状态文件采用 0600 原子替换，存选中的后端、续聊 ID 和最近 100 条已处理事件 ID，不保存用户消息全文。停止服务可发送 SIGTERM；WebSocket 关闭后等待已进入回调的任务完成，进程管理器的终止宽限期应大于最长任务时间。

## 当前边界与操作注意

- 每个会话同时仅执行一个任务；全服务最多 8 个任务，每轮最多 10 分钟。并发时会提示稍后重试。
- 处理完成会先持久化续聊 ID 和事件去重标记，再回复飞书；如果回复失败，重发相同事件不会重复执行后端，但该回复也不会自动补发。失败/超时后的后端是否已经执行无法仅凭飞书提示判断，应检查后端状态。若后端续聊 ID 已失效，网关清空该后端的旧 ID 并提示用户确认后重新发送；不会自动重放原请求。
- 这是单实例 MVP：原生菜单可用于控制操作，但没有多副本分布式锁、后台作业队列、卡片选择器、流式更新、媒体输入、跨后端协作或飞书侧审批。如果需要这些能力，应把工作队列、授权和审批设计为下一阶段。
- `@codex` 为文本别名，不会在飞书输入框的原生 @ 候选中展示；在一个机器人身份下无法伪装成多个原生机器人。

## 本地验证

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

协议依据：[飞书官方 Go SDK Channel](https://github.com/larksuite/oapi-sdk-go/blob/v3_main/doc/channel.md)、[Codex 非交互模式](https://learn.chatgpt.com/docs/non-interactive-mode)、[Hermes API server](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/api-server.md)。

## 首次配对与真实后端测试

首次取得发送者 `open_id` 时，使用独立的一次性配对工具，而不是把网关白名单改成允许所有人：

```sh
# 从已配置的 .env 导入应用凭证后执行。token 必须是随机且至少 16 位字母数字。
mkdir -p .local; chmod 700 .local
export FEISHU_ENROLL_TOKEN="$(openssl rand -hex 16)"
export FEISHU_ENROLL_OUTPUT="$(pwd)/.local/feishu-enrolled-user.txt"
go run ./cmd/enroll
```

工具只接受真实用户发来的私聊文本 `/pair <token>`，不会调用 AI，其他消息均忽略。配对成功后独占创建 0600 的身份文件并回复配对结果；现有身份文件不会被覆盖。将该文件中的 ID 配入 `FEISHU_ALLOWED_USER_IDS`，再启动正式网关。配对监听最多 15 分钟。

真实后端测试是显式选择的，普通测试不会调用付费模型：

```sh
# 必须先设置真实 Codex/Hermes 后端；会发出实际模型请求。
set -a; . ./.env; set +a
GATEWAY_LIVE_TEST=1 go test ./internal/agents -run 'TestLive(Codex|Hermes)$' -v -count=1
```

测试分别检查两个后端能完成首轮回答，并在续聊中记住首轮给出的标记。测试过程中禁止模型使用工具。

真实联调使用独立、未纳入版本控制的凭据与运行状态；不要提交认证材料、私有日志或临时运行配置，也不要把本地测试启动方式当作生产部署。

## 飞书原生菜单

可以在机器人设置中配置以下菜单，响应动作选“推送事件”。Go 网关通过原来的长连接订阅 `application.bot.menu_v6`，不增加公网回调地址。

| 菜单 | 事件 Key | 对应操作 |
|---|---|---|
| 选择 Agent → 使用 Codex | `agent_codex` | `/use codex` |
| 选择 Agent → 使用 Hermes | `agent_hermes` | `/use hermes` |
| 选择 Agent → 查看 Agent 列表 | `agents_list` | `/agents` |
| 新建会话 | `session_reset` | `/reset`，仅清空当前 Agent 的续聊 ID |
| 帮助 | `gateway_help` | `/help` |

菜单仍然验证 `FEISHU_ALLOWED_USER_IDS`，点击菜单不等于授权。文字指令及 `@alias` 保留作为兼容入口。菜单事件没有 chat_id：服务优先复用已有的私聊会话；首次使用时按 operator open_id 发一条初始化消息，取得飞书返回的真实 chat_id，再执行操作。不会伪造会话 ID，也不会改变原有状态文件键。

## 如何快速接入 Agent

### 已支持的类型：配置即可添加实例

设置 `AGENT_CONFIG_PATH` 指向 JSON 配置（格式见 `agents.example.json`），可以只加载一个 Agent，也可以为同一种适配器注册多个名称。配置只记录**环境变量名**，真实认证材料仍由服务端环境提供；修改后需要重启网关，没有热加载。

- `type: codex`：运行本机 Codex CLI。`repo_env`、`home_env` 必填；`binary_env` 可选，未提供时使用 PATH 中的 `codex`。
- `type: hermes`：连接 Hermes API。`base_url_env`、`api_key_env` 必填；`model_env` 可选，未提供时使用 `hermes-agent`。
- 名称必须符合 `[a-z][a-z0-9_-]{0,31}` 且唯一。例如 `codex_review`、`hermes_ops`。

例如，在配置的 `agents` 数组中增加：

```json
{
  "name": "codex_review",
  "type": "codex",
  "binary_env": "CODEX_BIN",
  "repo_env": "REVIEW_CODEX_REPO",
  "home_env": "REVIEW_CODEX_HOME"
}
```

配置对应环境变量后重启，该实例自动出现在 Agent 列表中，可用 `@codex_review 问题` 或 `/use codex_review`。会话按实例名称隔离。若需要原生菜单快捷按钮，再添加事件 Key 为 `agent_codex_review` 的菜单项；服务器不需要新增专用 handler。原生菜单数量受飞书平台限制，**本版不自动扩展或发布菜单**。

未设置 `AGENT_CONFIG_PATH` 时，保留原来的 `CODEX_*` / `HERMES_*` 两后端启动方式。

### 新协议：首次写适配器，之后配置注册

新 Agent 不能仅凭一个模型 API key 就等价接入。适配器需要处理真实的任务提交、认证、续聊、输出和失败语义，并实现现有接口：

```go
type Agent interface {
    Run(ctx context.Context, prompt, continuation, scope string) (
        answer, nextContinuation string, err error,
    )
}
```

在 `internal/agents` 中实现和测试适配器，再在 `internal/registry` 添加对应类型的构造规则。飞书收发、菜单、用户鉴权和会话存储无需重新实现。`scope` 是网关生成的稳定隔离键；只有成功的任务才返回新的 continuation。

当前只支持 `codex` / `hermes` 两种类型，不伪装支持所有 OpenAI-compatible 服务、任意 CLI 或 A2A/ACP。只提供 `/v1/chat/completions` 的模型服务，不能直接替代现有 Hermes 的 Responses 会话协议。

## 模型与思考强度设置（版本更新后生效）

在飞书原生菜单打开 `参数设置 → 选择模型` 或 `参数设置 → 思考强度`，机器人会发送可交互的选项卡片。Codex 模型选择白名单仅包含 `gpt-6-luna`、`gpt-6-sol`；未手动选择模型时仍使用服务端配置的默认模型（当前部署为 `gpt-5.6-luna`），本次白名单调整不修改该默认值。选择模型时会清空**该 Agent**的续聊 ID，新问题创建新会话。思考强度提供 `low`、`medium`、`high`、`xhigh`，未设置时默认 `high`；调整思考强度从下一轮生效，不自动重开会话。设置按飞书用户、私聊与 Agent 隔离，保存在同一 `state.json`，兼容旧状态文件。保留 `/model <白名单模型>` 和 `/effort <白名单强度>` 作为备用文字入口。

卡片回调必须验证操作者、真实私聊、当前 Agent、回调事件 ID、白名单选项和卡片创建时的设置快照；失效卡片不覆盖新的选择。卡片点击使用 `card.action.trigger` 回调，通过飞书现有长连接接收，不设置公网 HTTP 回调地址。当前服务器未部署 Hermes 后端，暂不提供 Hermes 设置卡片。

飞书菜单建议保留三个一级菜单：

- `选择 Agent`：既有 Codex、Hermes 和列表。
- `参数设置`：`选择模型` (`settings_model`) 和 `思考强度` (`settings_reasoning`)。
- `更多`：`新建会话` (`session_reset`) 和 `帮助` (`gateway_help`)。

首次新增上述原生菜单与回调需要发布飞书应用版本；后续只调整服务端模型白名单，重启网关后重新打开模型卡片即可，不需要重新发布飞书应用。部署前在 `go-runtime` 镜像中使用 opt-in live 测试验证两个白名单模型的首轮与续聊，以及 `high` 到 `medium` 的思考强度切换。
