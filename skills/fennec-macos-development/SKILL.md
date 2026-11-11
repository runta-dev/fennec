---
name: fennec-macos-development
description: Build, sign, restart, and debug the Fennec macOS companion, shared-channel session operations, UI state synchronization, Keychain access, cloud takeover, and CLI launch errors. Use for Fennec macOS development or recurring signing, session latency, and takeover problems; not for unrelated Runta runtime work.
---

# Fennec macOS development

Preserve existing source changes, user credentials, cloud sessions, and unrelated processes. Do not rebuild credentials, reset model preferences, or blindly retry an uncertain launch.

## Reuse the local signing identity

Development builds must use the existing `Fennec Local Development` certificate in the user's login Keychain. Never switch back to ad-hoc signing or generate a new certificate for each build: changing the signing identity can make macOS request Keychain authorization again.

```sh
./scripts/create-development-identity.sh
go build -trimpath -o /tmp/fennec-local-build/fennec-daemon .
FENNEC_DAEMON_BINARY=/tmp/fennec-local-build/fennec-daemon ./scripts/build-macos.sh
codesign -dvv build/Fennec.app 2>&1 | grep Authority
```

The creation script is idempotent. It must return the existing identity rather than importing another certificate. The expected signature authority is `Fennec Local Development`. The packager defaults to this identity; `FENNEC_SIGN_IDENTITY=-` is only an explicit ad-hoc escape hatch, not the normal development workflow.

The first use of a newly imported private key may require the user to allow `/usr/bin/codesign` in the system authorization dialog. Do not bypass the dialog, weaken Keychain ACLs, or allow every application to read credentials. After that one-time authorization, rebuild once more and verify that signing finishes without another prompt.

The signing key and the Runta login token are separate Keychain items with separate ACLs. An existing `com.runta.fennec` token created by an old ad-hoc build can still prompt after the signing identity is fixed. Ask the user to choose **Always Allow** once for the current stable-signed app, then restart that app and verify silent credential restoration. Never claim this migration is complete based only on a successful codesign or a new build without a signing prompt.

Do not replace the certificate merely because `security find-identity -v -p codesigning` reports it as untrusted. A self-signed development certificate is not a Developer ID distribution certificate. Release builds still require a stable Developer ID Application identity and separate notarization/update verification.

## Restart without losing cloud work

Self-signed certificates do not alone stabilize Keychain partition IDs: the login item can retain a cdhash partition even when its trusted-app requirement accepts the certificate. Credential operations now run through `fennec-credentials`, which validates its parent as Fennec signed by the same certificate. Preserve the cached helper binary across ordinary app rebuilds; compare its SHA-256 before/after. Recompiling the helper itself changes the Keychain access identity and can require authorization again. Do not reset credentials or loosen partition restrictions to hide this.

Build into a staging directory, verify the complete signature, stop the running Fennec app, then install the bundle at the existing canonical path. Never overwrite running signed binaries in place. Verify credential restoration after the new app starts; a successful build proves neither Keychain access nor broker readiness.

UI 和 Go 控制面必须来自同一份源码快照。禁止只更新原生 UI、继续沿用缺少新接口的旧 Go artifact。打包脚本运行 `cmd/check-control-contract` 验证界面所调用的方法，并把签名后二进制哈希写入 `Contents/Resources/control-build-id`。`fen start` 会校验后台的 build ID，串行替换旧 LaunchAgent；仅重启 UI 不等于后台已经更新。更新后必须实际调用新接口，不以 App 已打开作为验收。

Stop only the task-owned Fennec application process, then open `build/Fennec.app`. Do not terminate remote Herdr sessions or assume application restart deletes cloud work. Reinstall shims only if their daemon path is stale.

After restart, inspect:

- `~/.config/fennec/preparation.json` for preparation stage and runtime ID.
- `~/.config/fennec/route-results.json` for each agent's actual route status.
- `~/.config/fennec/connector-status.json` for a live connector.
- `fen daemon status` and `fen takeover` for the independent Go service and active takeover. Broker and connector lifecycle now live in that process.

Do not call the app fully ready solely because it launched or the connector is connected.

## Diagnose takeover before launching

Reuse any existing cloud agent binary. Install the publisher's `latest` stable release only when the cloud binary is missing; do not query or upgrade an already installed agent during preparation. Never synchronize, compare or require the local CLI's exact version for a new cloud session. The local executable is only a discovery target. Existing sessions retain their original executable; staged installation and atomic alias replacement protect them from failed first-time installations.

`Waiting for cloud takeover` with an enabled switch can be a stale preference, not an active broker. An agent is eligible only when its route status is `configured`. If it is not configured, the UI must show that reason instead of claiming takeover is pending.

Normal preparation synchronizes credentials and model configuration and verifies configuration readback. Do not send inference probes or use provider quota, temporary rate limits, or model-catalog health to gate TUI startup. Inference probes belong only in explicitly requested diagnostics; model request errors are presented by the agent during actual use.

Use a fresh, valid `provider/model` route when the configured model has actually been removed. Do not silently fall back to local execution or invent a replacement. Verify the resulting route becomes `configured` and that `launch-broker` is running.

## 会话操作必须复用常驻 channel

用户后续要求：模型目录加载、Magpie 切换等日常交互操作的端到端 wall time 必须在 1 秒以内；既有会话热路径继续执行更严格的 500ms 门槛，冷 SSH/首次安装单独计量。以实际数据可用或配置生效为终点，不能用提前回执、预览 fixture、旧缓存或缩短超时伪装达标

用户的一台共享云机器由一个 Go daemon 管理。UI 和 CLI 使用同一个控制面；launch、resume、stop/kill、read、send、list/refresh 都必须检查实际传输路径，不能只优化启动。

- 正常操作复用 daemon 已验证身份、运行时归属及主机密钥的常驻 SSH connection **和常驻协议 channel**。复用 SSH ControlMaster 但每次再执行 `ssh ... sh -s` 仍有新 exec channel 的开销，不算完成复用。
- 不在 UI handler、CLI 子命令或点击后的刷新里重复跑 `OwnedRunningRuntime → Connect → NewSession`。初次连接、账号/运行时切换、连接失效时才由 daemon 单飞完成验证和重连；不要以性能为由移除身份及主机密钥校验。
- 常驻 channel 使用带 request ID 的类型化操作及独立回包；慢会话不能阻塞其他操作。stop 必须验证远端 `fennec.session` label 与目标 ID 一致，并得到结束确认。失败或回包丢失不能自动换一条路径重试 mutation。
- 操作结果直接更新 daemon cache 和 UI。stop 确认后立即移除对应行，不等待另一轮远端 list；失败则保留行并显示未确认。用 revision/generation 或等价机制阻止旧刷新把已停止会话重新显示。
- 新增或修改操作时检查 Go handler、IPC、远端协议、UI 回调的整条链路。协议有新增能力时处理版本兼容与运行中的 daemon/远端 artifact，不能只改源码就宣称生效。

验收必须区分：点击/CLI 调用到确认的 wall time、已有 channel 网络往返、远端本地执行、确认到 UI 更新。用自己创建的有标签测试会话，不停止用户工作会话。检查稳定运行和重连中的错误语义、并发操作、迟到刷新；有 warm channel 时不应新增 SSH 进程或 exec channel。`cmd/profile-stop` 可测 fresh SSH、resident channel 和实际本地 CLI 路径，测量边界必须明确，不能把 transport-only 数字称为 UI 端到端耗时。

本次故障的证据：相同远端 stop 执行约 6–10ms，fresh SSH 路径约 4.01–4.36s，resident channel 约 274–282ms；旧 UI 在 stop 后还等待完整远端 list。数值是当次测量，不是固定性能保证；保留上述结构性约束。

运行 `make test-e2e-ops` 对安装后的真实路径验收。每个热会话操作样本必须 ≤500ms，冷 SSH 连接单独测量（约4秒，测试上限5秒）。错误、无效回包、超时、超预算都必须让测试非零退出；不能用平均值、缓存列表代替远端刷新、回执代替可输入 TUI，或缩短请求超时来制造“变快”。端到端失败时不得宣布性能目标完成。正常单元测试不连接用户云机器；该显式 live target 缺少登录态或 runtime 时应失败，不能 skip 后报告通过。

## 产品界面与内部诊断

### UI 文案与控件

- UI 样式迭代优先使用 `make preview-ui` 本地开发预览，直接读取 UI 源码，刷新即可查看；不为每次视觉调整执行完整 DMG 打包安装。预览使用隔离展示数据，不改账户或云端状态，不能替代真实认证、性能或正式包验收

- Fennec 不接管原生 Agent 账号管理，已登录时只展示订阅信息和刷新，不提供 Sign in again 或账户切换；未登录时可提供 Sign in，通过现有浏览器 session 通道打开原生 Agent 登录会话，不在面板内嵌登录管理界面
- 不使用通用失败/Please retry toast 处理后台读取或瞬时连接状态，先修复失败根因；后台读取保留已有内容并恢复，用户操作的失败反馈必须贴近相关控件，说明已知原因与具体恢复动作，不能让用户盲目重试

- 正式 UI 不提供 Uninstall Fennec 入口或卸载弹窗，`fen uninstall` 保留为 clean macOS 验收和测试环境清理工具，不作为正式设置项；独立删除云电脑的危险确认属于另一个产品操作

- Magpie 首次初始化时根据实际安装情况决定默认值：本机已安装则开启，未安装则关闭，保存过的选择始终优先；配置或缓存残留不能作为安装证据，用户手动关闭后不自动重开
- Magpie 关闭时，Agent 展开面板展示原生账户与真实订阅信息，不受本机/云电脑运行位置影响；缺少可迁移原生凭据静默跳过，在 Agent 卡片显示「未登录」，不在会话页显示全局凭据错误或无效重试
- Magpie 开启时展示 Magpie 模型选择，不把原生订阅卡片与网关模型状态混为一谈；无订阅或额度数据时不推断或伪造套餐、用量、剩余额度

- 面向用户的 UI 文案不使用句号，覆盖标签、说明、tooltip、状态、错误提示、弹窗及所有语言翻译
- 多句说明改写为短语、逗号或分行，不仅移除中文句号，还要同步移除英文等语言作为句号的 `.`；保留版本号、网址、文件名、代码和原样终端输出中的点
- 例如使用「为云端智能体自由选择模型」和「Choose any model for your cloud agents」
- Effort 滑杆参考 Magpie 的细灰轨道、离散刻度点与白色圆形滑块，适配 Fennec 的字体、间距、主题色与键盘焦点样式，不使用浏览器默认的粗橙色进度条
- 遵循 let UI explains itself：保存模型、切换设置等操作通过原控件的选中、禁用或局部 loading 状态反馈，不新增「正在保存模型选择…」等解释操作过程的文案或全局 notice
- 异步反馈不得引起 layout shift：保持原控件与容器的宽高、间距和位置，loading 如需替换图标须复用预留空间，不插入状态行、不增删占位区、不让按钮因文案变化伸缩
- 保存期间防止重复提交，成功后展示服务端确认的状态，失败时恢复可操作状态并提供简短、可行动的错误反馈，不把未确认的选择当成已保存
- 验收须实际比较操作前、进行中、完成和失败四种状态，检查整个面板没有跳动，不能只检查静态截图

不要把 `CLI integration needs repair`、原始 RPC/daemon 状态码、接口不存在等工程问题显示成用户待办。恢复的开关、异步准备结果和真正的入口检查是不同事实：尚未收到检查结果不能判定损坏。daemon 负责去重检查和自动修复，详细原因写入私有诊断记录；界面保留已有有效数据，正常恢复过程使用 loading。用户确实需要登录、授权或选择模型时才提供相应操作。修复必须包括根因与回归测试，不能仅删除报错文案。

## Preserve actionable launch errors

Interactive cloud launches show only a transient single-line spinner with no text on stderr and erase it before attaching the agent. Do not print session IDs, reconnect commands, phase logs or other debug text during a successful launch. Pipes and noninteractive shells stay silent; failures retain a concise actionable error. Preserve terminal raw-mode handoff by clearing the animation before `exec`.

Do not replace broker or project-launch failures with only `launch not confirmed`. Preserve the safe underlying error through `internal/launcher/broker.go` and `internal/cli/launch_broker.go`. Never include API tokens, credentials, or unbounded server responses in these errors.

A pending launch journal entry has an uncertain outcome. Inspect the corresponding cloud session before retrying; do not delete the journal or repeatedly create replacement sessions. If a receipt exists, reconnect to that session.

## Verification

Run focused tests for changed packages and `make check-ui`. macOS lacks GNU `flock`, so Linux-specific setup tests can fail locally; report that boundary rather than treating it as a regression or weakening production locking.

Before reporting success:

1. Confirm the app uses `Authority=Fennec Local Development`.
2. Rebuild and observe no repeated private-key authorization prompt.
3. Confirm the requested agent route is `configured`, connector is connected, and launch broker is active.
4. Exercise a noninteractive command such as `codex --version` through the normal login-shell PATH. Argument validation alone does not prove a cloud launch; an `ls` of the local directory is also not remote evidence.
5. Report any unverified interactive terminal behavior separately.

Standalone `codex --version` is explicitly dispatched locally by `LocalQuery` and does not verify cloud execution. For cloud acceptance use `fennec-daemon run-agent codex --version`, or obtain a real launch receipt through the broker and verify the corresponding remote Herdr session and output.
