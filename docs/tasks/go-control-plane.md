# Go 统一控制面迁移

## 验收边界

UI 与 `fen` 必须调用同一个 daemon API。关闭 UI 后，登录、配置、运行时准备、接管、会话管理、审批与账单操作继续可用。UI 不保存 token，不直接修改业务状态文件，不负责 broker/connector 的重试和生命周期。

Go daemon 管理一个用户的共享云机器与 SSH 连接；切换接管选项不应重建连接。退出 UI、退出登录、停止 daemon 是三个不同操作。账号切换必须取消旧账号工作，不能让迟到的响应写回新账号状态。退出或关闭终端不能杀死远端 agent。

## 已落地但尚未切换生产 App

- `internal/control`：版本化 Unix socket RPC、同用户 peer 校验、0700 目录、0600 socket、单实例锁、有限请求大小与并发、脱敏错误。
- `internal/account`：Go device flow、过期时间、slow_down、凭据保存/恢复/删除、账号 epoch、公共状态 revision；Keychain helper 仅作为平台适配器。
- `internal/controlplane`：公共账号状态、登录/退出、agent 列表、状态长轮询、API 方法发现。
- 账号资料与账单聚合已迁入 Go（独立账单请求并行），只输出公共字段。
- `runtime.ensure/status` 由 daemon 单飞执行准备，账号切换取消任务；`sessions.list/launch/read/send/stop` 从 daemon 注入 token 和 runtime，拒绝客户端替换身份。会话传输仍需接入共享连接。
- 原有 CLI 准备与会话实现已抽取为可复用函数；删除会话路径中不可达的嵌入式 Python。
- `control-daemon` 与 `fen control <method>` 接入上述 API；目前是显式入口，尚未自动启动或接管旧 App。
- socket、多客户端状态一致性、CLI 调用、凭据不出现在 snapshot、授权过期和迟到响应测试。

## 必须完成后才能宣布迁移完成

- [ ] Go 接管 runtime preparation 及 snapshot 持久化。
- [ ] Go 接管 takeover/model 配置，保留 selected 与 ready 区别。
- [ ] broker/session service 归属 daemon；配置变化不重建共享 SSH。
- [ ] Go 接管 connector/reverse link、退避重试、审批队列与 fingerprint trust。
- [ ] 账号 profile 和 billing 查询及聚合迁入 Go。
- [ ] session list/start/resume/read/send/stop 统一业务处理。
- [ ] 所有 UI 操作对应明确 CLI 子命令与 JSON 输出。
- [ ] Swift 删除业务编排，保留 AppKit/WebView、浏览器、剪贴板与终端展示。
- [ ] LaunchAgent 自动启动与生命周期；UI 退出不停止 daemon。
- [ ] 签名 daemon 获得 helper 的受限调用资格；复用稳定 helper，验证重启/重建无重复密码框。
- [ ] GUI 关闭的真实 CLI 验收及双客户端状态一致性验证。
- [ ] 重新验证真实 TUI 冷/热启动与 resume；不能用回执时间代替可输入首屏。

## 当前验证记录

- `go test -race ./internal/account ./internal/control ./internal/controlplane ./internal/cli` 通过。
- `go build ./...`、签名身份解析工具测试、`make check-ui` 通过。
- staging bundle：`/tmp/fennec-control-stage.MM9WVi/Fennec.app`，完整签名验证通过，daemon 满足现有 helper 的证书与 identifier requirement。
- staging 与当前 App 的 helper SHA-256 均为 `b0ee08622cd866bf1e1bfd7c5f7f76ba7eca51944364e329a6d47a33e3714d6e`，helper 未重编译。
- 尚未替换运行中的 App，尚未验收新 daemon 的实际 Keychain 恢复、GUI 关闭操作或所有权切换。静态签名验证不等于上述验收。

本文件是迁移清单，不表示这些未勾选能力已实现。
