# OpenAI OAuth Codex 出站身份重构：只放官方客户端 + 拟真派生

> 状态：设计讨论稿（proposal/design 已定稿方向，tasks/specs 待 Open Questions 实测后补齐）。

## Why

OpenAI OAuth（codex 协议）账号的出站请求现状有四个结构性问题：

1. **UA 是全网关级假指纹**。`enforceCodexIdentityHeadersWithUA` 默认把每个 OAuth codex 请求的 UA 无条件钉成 `codex-tui/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color`（`openai_gateway_service.go` 编译期常量 + 面板/自动同步版本）。Mac 用户自称 Ubuntu，所有下游客户端收敛成一个串，且所有 sub2api 部署共享同一精确 UA——上游拉黑该字符串即可一键识别网关。这是当前最"假"的信号（installation-id 在默认 fingerprint off 下已按 account 派生，并不假）。
2. **两套身份系统重叠**。`openai_codex_account_identity.go`（默认开，按 account+apiKeyID 派生）与 `openai_codex_fingerprint.go`（账号 opt-in 的 device/session/full 收敛）语义交叠，且后者的收紧端有实锤事故：v0.1.175 曾把收敛设为默认，触发额度缩水（#5553/#5555/#5556/#5582/#5610，有"回退即恢复"的 A/B 实测），v0.1.176 改回显式 opt-in。
3. **为非官方客户端维护大量桥接整形**。`applyCodexOAuthTransform` 里的 instructions 合成、functions→tools 迁移、system message 提升、call_id 归一化等，全部服务于把非 Codex 客户端的请求伪装成 Codex 形态。
4. **WS 池握手身份不一致**。fingerprint off 时 compatibility key 只含 betaFeatures，连接跨用户共享，握手头（UA/installation/session-id）永远停留在"创建连接的那个用户"。

## What Changes

基本方向：**只放行官方 Codex 客户端，不做伪装；仅替换认证；身份派生以"每个伪设备 = 一台真设备的改号版"为目标。**

- **客户端门（默认开启，不可对 OAuth 账号关闭）**：仅放行官方 family——CLI（`codex_cli_rs`/`codex-tui`/`codex_exec`）、Desktop（`codex_chatgpt_desktop` + `Codex ` 家族）、VSCode 扩展（`codex_vscode`/`codex_vscode_copilot`）；app-server 客户端按现有白名单 + 引擎指纹门放行，未列名者归入单一 app-server family。非官方客户端直接拒绝（明确文案引导，不伪装）。
- **UA / originator / version 头：校验后原样出站，不做任何归一化**。UA 形态校验 + per-family 版本下界（CLI ≥ `codexUpstreamMinVersion` 0.144.0，低于上游 404）；originator 与 UA 首段配对校验（issue #3901）。删除 canonical UA 强制改写默认、`ForceCodexCLI`、账号级 UA 覆写。
- **installation-id 三档**（每账号可切换，纯服务端派生、切档不改协议）：
  - 默认档：`derive(account_seed, 客户端真实 installation_id)`——设备形态完全拟真（并发/会话/作息等于单个真实用户），无跨账号泄漏；不加控基数（真人设备数本就偏高：多机器/devcontainer/CODEX_HOME 可任意新建）。
  - 信任档：原样透传（单租户/信任下游场景；文档标明牺牲跨账号防关联）。
  - 收紧档：`hash(class, install_id) mod K` 的 K-bucket（class = family + OS 种类/大版本 + arch，K≈4~8），封顶设备数并压平新增设备 churn，接受每设备并发上升。
  - 现有 device/session/full 收敛档进入兼容期 deprecation。
- **持久 ID 派生、一次性 ID 透传**：session-id / thread-id / window-id / prompt_cache_key 按 `derive(account_seed, client_value)` 派生（**去掉现有实现混入的 apiKeyID**——官方客户端的 uuid 不会跨用户碰撞，apiKey 隔离不再必要，去掉后同一会话回切同账号时缓存语义纯净）；turn-id、x-client-request-id 每次唯一、不可能跨账号重复，直接透传（同时天然保证 header 与 body 一致，删掉现有"头体同步生成 turn_id"的复杂度）。
- **WS**：handshake compatibility key 无条件纳入 installation-id（同桶 = 同一伪设备，握手身份自洽）；UA 不进 key。WS ingress 默认从 ctx_pool 切到 `dedicated`（每下游连接一条上游连接，最拟真形态）；ctx_pool 保留给 HTTP 起源请求与资源优先场景。
- **删除 OAuth 路径桥接整形**（instructions 合成、functions→tools、system 提升、call_id 归一化、模型别名映射的 OAuth 分支等），仅保留：认证替换、版本门槛校验、`guardOpenAICodexTurnStateEcho` 跨账号 turn-state 剥离、chatgpt-account-id/Host 头、`store=false` 防御性保留。
- **观测**：usage log 记录出站身份摘要（派生 installation 前缀、family、平台）；每账号 distinct installation 数、新增设备 churn、每设备并发会话数三个指标与告警——用于验证"设备基数不控"假设（已知事故都在超人设备端，基数端无记录但从未以真实 UA 跑过生产）。
- **配置收敛**：`ForceCodexCLI`、`gateway.disable_codex_identity_enforcement`、账号 UA 覆写、`codex_cli_only`+全局 policy、fingerprint mode 等五个交互开关收敛为单一 policy，旧的进 deprecation。

## Capabilities

### New Capabilities
- `codex-official-client-gate`：OAuth codex 账号的官方客户端门（family 枚举、UA 形态与版本校验、app-server 白名单与引擎指纹、拒绝文案）。
- `codex-outbound-identity`：出站身份派生（installation 三档、持久 ID 派生、一次性 ID 透传、UA 校验后透传、三路径单点收口）。
- `codex-ws-identity-pooling`：WS 连接身份一致性（桶 key、dedicated 默认、HTTP 起源池化）。

### Modified Capabilities
<!-- openspec/specs 目前为空。 -->

## Impact

- **BREAKING**：OAuth codex 账号不再服务非官方客户端（普通 OpenAI SDK / chat-completions 桥接流量收到明确 403 引导文案）；此类流量需迁移到 API Key 账号。
- **BREAKING**（一次性）：上线后所有账号出站 installation-id 集体切换，上游看到一次性"换设备"churn，需按账号灰度，不与其他大改混发。
- **后端**：`openai_codex_fingerprint.go`（重构为派生核心）、`openai_codex_account_identity.go`（并入派生核心，去 apiKeyID）、`openai_codex_identity.go`（校验替代改写）、`openai_codex_transform.go`（大幅删减 OAuth 分支）、Forward / passthrough / WS 三条路径的收口点、`openai_ws_pool.go` compatibility key、`openai_client_restriction_detector.go`（默认化）、setting 相关（配置收敛与 deprecation）。
- **上游影响**：出站 UA 从单一假串恢复为真实分布；WS 连接数上升至 ≈ 下游并发会话数（等于这些用户原生使用时的连接量）。
