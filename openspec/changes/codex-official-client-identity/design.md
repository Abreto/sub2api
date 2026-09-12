# Design

## Context

动机见 proposal.md。与方案相关的现状与关键证据：

- **UA 收口**：`enforceCodexIdentityHeadersWithUA`（`openai_codex_identity.go`）对 OAuth codex 出站无条件把 User-Agent/originator/version 重建为规范身份（`codex-tui/{version} (Ubuntu 22.4.0; x86_64) xterm-256color`）。这是当前最主要的假信号。
- **双身份系统**：`openai_codex_account_identity.go` 默认按 `(chatgpt_account_id, apiKeyID, kind, 客户端原值)` 派生全部标识（含 prompt_cache_key）；`openai_codex_fingerprint.go` 提供账号 opt-in 的 device/session/full 收敛（含 turn_id 头体同步生成）。二者在 installation/session/thread 上语义重叠。
- **v0.1.175 事故**：收敛默认开 → 额度缩水（#5553/#5555/#5556/#5582/#5610），回退即恢复。归因 caveat：当时 UA 同时是假串，无法完全剥离，但唯一有据可查的检测器开火在"超人设备"端（收敛），"设备数过多"端无事故记录。
- **installation-id 天然高基数**：`CODEX_HOME` 可任意新建，devcontainer/多机器的真人用户本就有多个安装；上游可用的强信号是 installation→多账号 关联图，而非单纯数量。
- **WS 池**：`normalizeOpenAIWSHandshakeCompatibility`（`openai_ws_pool.go`）仅在指纹收敛非 off 时把 installation/session 纳入连接分桶；off 时连接跨用户共享，握手头停留在创建者。ingress 已有 `ctx_pool/shared/dedicated/passthrough/http_bridge` 模式（per-account 可配），`dedicated` = 每下游连接强制新上游连接。
- **客户端门**：`codex_cli_only` 检测器（`openai_client_restriction_detector.go`）已具备 strict UA 前缀、originator 精确集、min/max 版本、引擎指纹 AND 门、黑/白名单，目前是账号 opt-in。
- **UA 收敛的引入历史（2026-08 考古，git 考证）**：收敛不是反指纹措施，是 2026-07 底上游容量降载事故的止血方案。时间线：02-07 `ce764bf2d` ForceCodexCLI（显式"伪装 UA"）→ 07-10 `8a51119e3` #3901 配对 404 → 08-02 `e1b76e224` 上游按 **originator 桶**降载（07-29 起 codex-tui 入桶、codex_cli_rs 正常，且 codex_cli_rs 配 curl UA 亦可正常返回——证明降载因子是 originator + 版本新旧，不是 UA 的 OS/arch/terminal 多样性），网关把 `server_is_overloaded` 误判为账号故障导致冷却风暴 → 08-03 `2eb24814f` 全员统一 canonical 身份 + 3h 自动同步版本 → 08-07 `dbb42881c` 默认身份 codex_cli_rs → codex-tui 再翻转（追着动态降载桶跑）。两个推论：① 保留真实 UA（OS/arch/terminal）+ 官方 originator + 较新版本不会比统一身份更触发降载；② 统一身份有单点脆弱性——身份一旦入桶即 100% 流量被降载，真实身份分布天然只部分中桶。
- **调度粘性**：`GenerateSessionHash` 用客户端原始 session 信号做 sticky，与账号侧派生天然兼容（派生在账号选定后进行）。


## Goals / Non-Goals

**Goals:**
- 上游视角下，每个伪设备 = 一台真实设备的"改号版"：UA 全真、单设备形态拟真、无跨账号关联。
- 每层身份基数有结构上界或结构拟真，且每档可独立回滚（信任档透传 = 总回滚位）。
- 三条出站路径（HTTP Forward / passthrough / WS）单一收口，一套派生实现。
- 每账号可观测设备基数 / churn / 每设备并发，用于持续验证假设。

**Non-Goals:**
- 不为非官方客户端做任何伪装或桥接（直接拒绝）。
- 不做 UA 归一化（含系统版本）。
- 不解决账号级聚合负载/用量的隐藏——那是容量问题，靠调度侧并发预算与加号解决，identity 方案不背这个目标。
- 不改动非 OpenAI 平台与非 codex 协议路径。

## Decisions

### D1：只放官方客户端，门默认化
- family 枚举：CLI = `codex_cli_rs`/`codex-tui`/`codex_exec`（同一二进制的交互/非交互形态）；Desktop = `codex_chatgpt_desktop` + `Codex ` 家族前缀；VSCode = `codex_vscode`/`codex_vscode_copilot`（显式产品决策：扩展是官方客户端，放行）。
- app-server：originator 是 `clientInfo.name` 逐字透传的任意串，按现有白名单（双因子 AND 引擎指纹）+ `AllowAppServerClients` 开闸放行；未列名 app-server 客户端在身份侧归入单一 `appserver` family（防止任意命名铸造派生基数），白名单列名者可独立成桶。
- 校验替代改写：UA 形态校验、originator↔UA 首段配对（#3901）、version ≥ per-family 下界（CLI 挂 `codexUpstreamMinVersion`，现有差异化文案"请升级 Codex"复用）。vscode/desktop 版本段语义不同源，先只做下界 + 形态校验，Max 卡太死会挡正常升级（现 policy 中 Max 本就可选）。
- 备选：继续 opt-in `codex_cli_only`。否决：伪装面（桥接整形）正是依赖"门不开也得能过"的存在。

### D2：UA 校验后原样出站，不做系统版本归一化
- per-install 派生（D3）下，每个派生 installation 对应一个真实安装，其 OS 版本/arch/terminal/版本全部真实自洽：版本漂移=升级、terminal 变化=换终端、OS 版本变化=系统升级，均合法。
- 归一化的三个否决理由：全网关部署归一到同一 OS 串会制造不自然的浓度聚类（恰好与归一化的初衷相反）；desktop UA 形态杂、归一化规则维护成本高；归一化只有配"每平台完全收敛"时才必需，而收敛已降为非默认档。
- UA 字段是否进 installation 派生 key 的结论（讨论演进记录）：family 进（不同 app=不同安装）；app version 不进（升级不变 id）；OS 版本不进（原地升级）；platform/OS 种类+大版本进（换机器才算新设备；Win10/Win11 必须分设备）；arch 必须进（一台机器不可能换 arch，混用即异常信号）；terminal 不能进（freeform 可控字段，轮换即可铸造伪设备）。
- 备选：class 派生（family+platform+arch）为默认档。否决为默认：两个用户共享同一 class 时产生 OS 版本 flapping（一会儿 Win10 一会儿 Win11），且把 N 个用户的会话压进少数设备 → 每设备超人并发（v0.1.175 模式）。class 保留为 K-bucket 收紧档的组成部分。

### D3：installation-id 默认 per-(account, install_id) 派生，三档结构
- 默认档：`derive(account_seed, client_installation_id)`。设备形态完全拟真；跨账号 failover 不泄漏（account 在派生里）；无 flapping；无需解析 UA。
- 派生密钥复用现有 `codexFingerprintSeed`（每账号已有随机 seed），无新状态。
- 去 apiKeyID 的前提 = D1 的官方门：官方客户端 session/installation 均为 uuid，跨用户碰撞可忽略，apiKey 隔离失去必要性。
- 基数风险的立场（见 D8）：已知事故在收紧端；默认站在拟真端，用观测验证基数端，K-bucket 档兜底。派生是服务端纯函数，切档零协议成本。
- 信任档（透传）：单租户/信任下游；显式文档标注牺牲跨账号防关联。
- 收紧档（K-bucket）：`hash(class, install_id) mod K`，K≈4~8，class = family + OS 种类/大版本 + arch（OS 大版本必须入 key，否则 Win10/Win11 flapping）。设备数 ≤ 活跃class数×K，churn 被压平，每设备并发 = 该 class 用户数/K。
- 备选：每平台收敛为默认。否决：v0.1.175 实锤 + 单设备超人形态。

### D4：一次性 ID 透传
- turn-id、x-client-request-id 每请求/每 turn 唯一，不可能在两个账号重复出现 → 无跨账号关联面 → 透传。顺带删除现有"头体共享同一份收敛 IDs 以同步 turn_id"的机制（透传天然一致）。
- prompt_cache_key 仅在等于 body `client_metadata.session_id`（官方默认行为）时跟随 session 派生；显式自定义值按 `kind=prompt-cache` 独立派生（沿用现有语义）。

### D5：持久 ID 派生与缓存兼容
- session-id / thread-id / window-id / prompt_cache_key = `derive(account_seed, client_value)`，window 跟 thread 走（保留 `thread:N` 形态，不硬编码 `:0`）。
- 缓存命中链：sticky 调度按客户端原始 session 信号哈希（不变）→ 账号选定后派生 → 同会话回切同账号时派生值不变、缓存仍热；failover 到其他账号缓存必 miss，是"不跨账号关联"的固有代价，接受。
- 三路径单点收口：Forward（map 版）、passthrough（raw 版）、WS（header + payload）共享同一派生核心，沿用现有"staged IDs + map/raw 双实现"模式但仅一份语义。

### D6：WS 身份一致性
- compatibility key 无条件纳入 installation-id（off 档也不例外）→ 同桶 = 同一伪设备，修掉"握手身份停留在创建者"的不一致。UA 不进 key：上游对一条连接只见创建者 UA，同桶设备 UA 自然一致；进 key 会让连接数乘上版本数。
- WS ingress 默认 `dedicated`（已有模式，`ForceNewConn`）：最拟真形态（真 codex 每会话自开连接）；连接总量 ≈ 下游并发会话 ≈ 这些用户原生使用时的量。ctx_pool 保留给 HTTP 起源请求（Forward 选 WS transport 时无下游连接可对应）与资源优先场景。
- 备选：池化 + 桶 key。保留为非默认：一条连接跑多个 session、握手 session-id 归属他人，是真实 codex 不会出现的形态。

### D7：整形删除清单与必须保留清单
- 删（OAuth codex 路径）：instructions 合成（`defaultCodexSynthInstructions`）、functions/function_call 迁移、system message 提升、call_id 归一化的桥接分支、spark 图像桥接指令注入、账号 UA 覆写与 canonical UA 重建。
- 留：认证替换（Bearer/chatgpt-account-id/Host）、版本门槛校验、`guardOpenAICodexTurnStateEcho`、`store=false` 防御性强制、模型归一化中承担路由/计费的最小集、reasoning include 补齐（官方对齐，幂等）。
- 非 OAuth 平台不动：API Key 账号继续服务通用 OpenAI SDK 流量，桥接整形对该路径保留。

### D8：基数风险的观测优先
- 两个方向相反的信号不可同时消除：设备基数/churn（随用户量线性涨）与单设备形态（随设备数收紧而超人化）。已知检测器在后者开过火；前者无记录但「真实 UA + 不控基数」组合从未上过生产。
- 故默认拟真端 + 三指标观测（distinct installations / 新增设备速率 / 每设备并发会话）+ 告警 + K-bucket 档兜底。账号聚合负载的隐藏不在本方案目标内（调度侧并发预算已存在）。

### D9：配置收敛
- 新单一 policy（OAuth 账号）：客户端门（family + 版本下界 + 引擎指纹开关）、installation 档位（default/passthrough/kbucket）、WS ingress 模式。
- 进入 deprecation：`ForceCodexCLI`、`gateway.disable_codex_identity_enforcement`、账号级 UA 覆写、`codex_cli_only`（被默认门吸收）、`codex_fingerprint_mode`（被档位吸收）。兼容期保留读旧配置映射到新 policy 的最接近档位。

## Open Questions（阻塞 tasks/specs，需实测或后续决策）

1. **vscode/desktop 与 cli 是否共享 installation-id**（是否共用 CODEX_HOME）：影响 K-bucket 档 class 语义与文档表述；不阻塞默认档。**已部分解锁（2026-09 实测样本）**：macOS Codex Desktop 真实 UA 为 `Codex Desktop/0.154.0-alpha.6.2 (Mac OS 26.6.2; arm64) unknown (Codex Desktop; 26.908.40834)`——独立 Desktop 的 originator 是 `Codex Desktop`（`Codex ` 家族，非 `codex_chatgpt_desktop`，后者属于 ChatGPT 桌面 app 内的 codex）；GUI 环境 terminal token 实锤为 `unknown`；尾部括号组是 clientInfo（app 名 + app build 号 26.x 形态，与引擎版本 0.15x 语义不同）。**已知缺陷**：canonical 组装链（`SetCodexUserAgentVersion`/`rewriteCodexUATrailerVersion`）会用 CLI 同步版本无差别覆盖 trailer 的 app build 号，产生自然界不存在的组合；desktop UA 应走直通分支原样透传，不做 canonical。UA 公式本身已由开源代码钉死（`{originator}/{引擎版本} ({os_type} {三段归一化版本}; {arch}) {terminal}`，os_info 3.14.0 语义化补零：15.5→15.5.0、22.04→22.4.0；WSL 下 WT_SESSION 透传使 `(Ubuntu …; x86_64) WindowsTerminal` 成为真实组合）。OAuth 与 API key 两种认证方式的 UA 同串（客户端构造与 auth mode 解耦，auth mode 只改变 base_url / Authorization / ChatGPT 专有头）。
2. **vscode 扩展 UA 形态与 installation 共享**：唯一剩余待实测项。
3. **K-bucket 的 OS 大版本粒度**：Windows NT 版本 10.0 同 cover Win10/Win11（build 区分），class key 取"OS 串归一化到已知枚举"还是"完整 OS 串"，需在实现时定（倾向：解析为白名单枚举，解析失败在门处拒绝）。
4. **灰度与一次性身份切换**：按账号比例灰度的具体机制（特性开关粒度：全局 / 分组 / 账号）。
5. **旧配置迁移映射表**：五个旧开关 → 新 policy 档位的精确映射与 deprecation 期长度。
