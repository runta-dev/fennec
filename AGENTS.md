# Fennec 项目约定

## 统一使用 Go

- 后续新增的业务逻辑、daemon、远端服务、会话与 PTY 管理、传输协议、诊断工具、性能采样、原型及测试统一使用 Go。
- 不以“临时脚本”“快速验证”“单文件测试”为由新增 Python 实现，也不要在 Go 或 shell 中嵌入 Python 来绕过此约定。
- 一次性工具也可用 `go run path/to/probe.go`；单文件测试可用 `go test path/to/probe_test.go`，需要同目录实现文件时显式列出，例如 `go test path/to/probe.go path/to/probe_test.go`。
- 在当前任务涉及的路径中，将已有 Python 原型或嵌入式逻辑迁移为 Go；不要把 Python 原型继续扩展为正式实现，也不要为迁移而改动无关功能。
- 现有 macOS 原生 UI 的 Swift/AppKit、WebView 的 HTML/CSS/JavaScript，以及必要的构建与打包 shell 保持各自用途；它们不是新增 Python 或其他脚本语言业务实现的理由。用户明确指定语言时，以用户要求为准。

## 启动与连接设计

- Magpie 首次初始化时检测本机实际安装情况，已安装默认开启，未安装默认关闭；保存过的开关选择优先，用户手动关闭后不得因重启或再次检测而自动开启。旧配置、缓存、模型名称不算安装证据。未开启时从各 Agent 自己的文件、环境或 Keychain 同步凭据，不安装、启动或探测网关。UI 与 CLI 使用同一 `settings.set` 控制面；开启后才允许云端跨模型切换。
- Magpie 关闭时，模型目录与设置必须由 Go 控制面按 Agent 的原生能力校验：Codex/OpenAI、Claude Code/Claude、Pi 已配置 provider。不得复用 Magpie 模型偏好；同步时清除跨厂商遗留模型，保留云端 TUI 设置及有效原生选择，不通过推理探测判断模型可用性。
- 原生凭据同步须覆盖没有安装 Magpie 的首次使用、各 Agent 独立失败、云端 OAuth 刷新保留、密钥链拒绝、路径与凭据隔离。不要复制整个配置目录、会话、hooks 或本机可执行命令；不要把 secret 放进 argv、日志、RPC 回复或版本库。

- 每个用户使用一台共享云机器，所有 cloud agents 在该机器内运行。一个 Fennec daemon 管理一条共享 SSH 连接，各会话通过独立 channel 复用；不引入多机器连接池或调度设计。
- 共享连接的初始化与重连合并为同一个等待结果，避免并发重复建连。继续校验身份、运行时所有权和主机密钥，保持会话隔离及未知启动结果的防重复机制。
- 所有会话操作（launch/resume/stop/read/send/list）经同一 daemon 控制面复用常驻协议 channel；仅复用 SSH ControlMaster、每次另建 exec channel 不算完成。禁止 UI/CLI 正常操作重复鉴权与 SSH 建连。操作确认后直接更新缓存与 UI，并阻止迟到刷新覆盖新状态。实现与验收遵循 `skills/fennec-macos-development/SKILL.md` 的“会话操作必须复用常驻 channel”。
- 不做 Agent 进程预热池；可以预热 SSH 连接及传输通道。冷连接、热连接和 Agent 自身启动分别测量，端到端计时必须到可输入的 TUI，不能把会话回执时间当作首屏时间。
- 云端已有对应 binary 就复用，缺失时才安装；不比较或同步本地精确版本。日常同步凭据与模型配置不发送推理探测，不以模型限流或额度故障阻止 TUI 启动。
- 正常启动只显示无文本 loading 动画，TUI 接管前清除；非交互输出保持干净，失败时保留必要错误。
- 会话热路径使用真实端到端测试执行 500ms 硬门槛；冷 SSH 单独计量。超时、失败或超预算必须非零退出，不能拿缓存命中或回执时间替代实际操作完成和可输入首屏。
- 产品界面不暴露 CLI 集成诊断、daemon/RPC 内部状态码或接口版本不一致。后台负责恢复与修复；未知检查结果不等于故障。详见项目 skill 的产品界面约束。

## macOS 构建验收

- 构建、签名与重启遵循 `skills/fennec-macos-development/SKILL.md`，复用固定开发证书及稳定的凭据 helper。
- 先在 staging 目录构建并验证签名，再停止旧 App、安装完整 bundle 并重启；不要覆盖运行中的已签名二进制。
- UI 与 Go artifact 必须来自同一源码快照并通过控制接口契约检查。使用 `fen start` 验证已安装版本对应的后台已就绪；不能只重启 UI 而留下旧 daemon。
- 保留用户未提交改动、现有凭据和云端会话。完成必须有实际启动路径的证据，不能只凭编译、测试或配置状态宣称可用。
