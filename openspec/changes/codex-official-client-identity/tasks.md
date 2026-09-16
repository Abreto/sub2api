# Tasks

## Phase 0：UA 专项修复

### 0a. 每账号稳定 canonical UA profile

- [x] profile 表与确定性派生（`openai_codex_ua_profile.go`：加权表 + `deriveCodexUAProfileSuffix` + `buildCodexProfileUserAgent`）
- [x] 身份解析链接入账号级 profile（`resolveCodexOutboundIdentityForAccount` / `enforceCodexIdentityHeadersForAccount`；面板已配置 > 账号 profile > 内置常量）
- [x] 面板「UA 已配置」判定共享 UA 缓存与 singleflight（`IsOpenAICodexUserAgentConfigured`）+ wire 注入
- [x] 出站路径迁移：HTTP Forward / 透传 / WS 握手 / alpha-search ×2 / 探针（account_usage）/ 模型列表 ×2 / Messages 桥接
- [x] kill-switch：`gateway.disable_codex_account_ua_profile`（默认 false = 派生开启）
- [x] 单测：确定性 / 表内分布 / 格式 / 优先级链 / kill-switch / 非 OAuth 回退

### 0c'. canonical UA 家族校验

- [x] `ValidateOpenAICodexCanonicalUserAgent`（仅 CLI 家族：codex-tui / codex_cli_rs / codex_exec）
- [x] 面板 UA 保存路径接入校验（`setting_update.go`）
- [ ] 账号级 credentials `user_agent` 保存路径接入同一校验（无 UI 表单，经 admin API/库写入，待定位唯一收口点）

### 收尾

- [ ] 用户操作：部署本版本后清除面板中手动配置的过渡 UA，启用每账号派生
- [ ] 上线观测：`server_is_overloaded` 比率、上游错误率；接受一次性「换设备」churn

## Phase 1：格子收敛 + 官方客户端门 + 身份派生（阻塞于 Open Questions 1/4）

- [ ] 入站 UA 分类落格器（os × family × arch × version 窗口，per-family 窗口）
- [ ] CLI 整串生成 / Desktop 窗口内透传 + OS 段归一化
- [ ] 版本窗口拒绝门（复用 MinCodexVersion + 自动同步派生，差异化文案）
- [ ] installation-id 按 `(family, os, arch)` class 派生（维度与 UA 格子一致）
- [ ] session/thread/window/prompt_cache_key 派生去 apiKeyID
- [ ] WS 池 compatibility key 无条件纳入 installation-id；ingress 默认 dedicated
- [ ] OAuth 路径桥接整形删除清单（D7）
- [ ] 五个旧配置开关收敛与 deprecation 映射（D9）
- [ ] 观测：每账号 distinct installations / 新增设备速率 / 每设备并发会话
