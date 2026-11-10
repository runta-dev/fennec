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
