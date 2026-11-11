# Go 统一控制面与性能验收

## 已实现并在本机运行

- Go daemon 独立持有账号、Keychain helper、运行时准备、broker、connector、审批、接管与模型偏好；Swift 是 Unix socket 客户端及原生平台适配器。
- `fen start` 后台启动并复用已有实例；LaunchAgent 与 UI 分离。已实测关闭 UI 后，账号恢复、connector、设置、接管、会话 send/read/stop 持续可用，daemon PID 不变。
- `.app` 中主界面为 `Fennec`，Go 程序为 `fen`；旧 `fennec-daemon` 路径是兼容链接。开发签名及缓存的 `fennec-credentials` 保持稳定。
- UI、常规 CLI 与兼容 read/send/list/attach/reconnect 入口调用同一控制面。常规会话操作走常驻协议 channel；非 TTY 也使用该通道。重连只连接已确认会话，不创建替代 agent。
- Go 管理背景列表缓存，stop 确认立即移除行，generation 防止旧刷新复活行。显式 `sessions.refresh` 等待远端结果，不把缓存命中算作远端刷新。
- 控制 API 契约和远端 terminal protocol 在打包时检查。`control-build-id` 与运行中的 daemon 对照，`fen start` 串行替换旧后台，避免新 UI 调旧接口。
- CLI 入口由后台检查和修复；只修复与原始备份一致的 Fennec wrapper，外部修改保留。旧 `/tmp/fennec-session-ui-validation` 引用已修复为固定安装位置。
- 凭据接收器已迁移到 Go。账号 ID 优先匹配；区分本地与远端基线；同账号并发刷新保留较新令牌，不中断整个环境准备；空 auth 与其他账号不能覆盖现有凭据。真实故障中云端 Codex 令牌比本地新 12 秒，已保留云端并恢复环境。
- 运行位置是用户意图，不再因模型准备尚未完成而禁用 Cloud 选择。选择 Cloud 后按需恢复准备任务，不重启已有共享连接。

## 性能硬门槛

`make test-e2e-ops` 是显式真实环境测试。需要已安装、已登录且连接就绪；缺少条件时失败，不静默跳过。普通单元测试不会连接用户机器。

热操作逐样本上限 500ms，包括远端刷新、send/read/stop、launch 回执与可输入 TUI、attach/resume 可用首屏。冷 SSH 单独测量，上限 5 秒。测试只操作自己创建的会话，验证字面输入到达、stop 已结束、缓存没有复活会话、daemon PID 与常驻 channel generation 未变化。任何失败或超预算均非零退出。

## 2026-11-11 当前证据与剩余问题

后续已部署冷连接流水线构建 `3afd9c6fa47284151bb67f221c68e8dd904fba840c99ccd33c7b33ee4066cb07`：普通 shared connection 将仅握手阶段与新鲜授权并行，授权失败会关闭自己的 master；密钥或主机身份变化会在授权后重新握手。远端 `serve --protocol=N` 在读 stdin 前拒绝不兼容版本，因此普通首次 channel 可提前排队请求，不再等待 ready 的跨网往返。`connection.status.ready` 仍只在握手及协议确认完成后为真。

最新安装版冷 TUI：5379 / 3917 / 5640ms，日志 `/tmp/fennec-cold-pipeline-installed.jsonl`。SSH 分段只记录固定阶段名，不保存原始 SSH debug 内容：地址准备6–29ms，TCP239–261ms，KEX完成952–981ms，公钥接受1906–1935ms，最终认证3212–4039ms。慢样本主要位于公钥接受之后，以及 SSH channel/exec 建立，远端工作目录与 agent 生成仍只有数毫秒。仍未达成稳定5秒。

对应全套热门槛 `/tmp/fennec-e2e-pipeline.log` 仍 FAIL：TUI321–350ms；send仍约790–910ms，一次resume508ms，启动回执568ms。常驻channel generation保持1。没有丢弃这些失败样本或放宽预算。

修正了 profiler 的动画检测：测试明确设置正常交互终端，清除继承的 `NO_COLOR=1`，并匹配 Fennec 的完整 spinner 序列，避免将 Codex 自己的 spinner 当成证据。真实已安装版验证 `spinner_visible=true`。

此前合批构建：`70ff0534783f1c3e6e0a0627594815e156a013a4cd9da5173d7af4a107a4e362`。

- 初始回执与首屏片段已合批并部署。完整 CLI 端到端 attach 为348 / 349 / 354ms，resume 为311 / 359 / 353ms；这轮未再出现之前的 resume 超时。
- 热 Codex 可输入首屏：321 / 364 / 324ms。远端刷新284–370ms，read296–303ms，stop342–347ms。
- 最新安装版的真实冷 Codex 首屏：4411 / **5251** / 4246ms。仍有一例超过5秒，不能宣布冷启动目标完成。该例握手到执行前约3.8秒，`ssh.receipt` 969ms；下一步继续压缩冷路径握手之后的 channel/exec/PTY 往返，已有 `serve-initial`/`StartInitial` 可作为实现起点，但不能在所有权验证前启动 agent。
- `send` 仍788–793ms；启动回执另有一次556ms。`make test-e2e-ops` 正确非零退出。最新日志 `/tmp/fennec-e2e-ops-batched.log`，最新冷启动 `/tmp/fennec-cold-installed-batched.jsonl`。
- 升级时 `launchctl bootout` 已返回、旧注册尚未消失的竞态已修复：等待注销后再注册新服务。真实升级首次 `fen start` 从15秒超时改善为0.93秒成功，随后普通启动复用同一 daemon。

以下保留前一轮数据用于比较：

已安装的终端信息预取版本：

- 真正冷启动到可输入 Codex 首屏：4828 / 4234 / 4181ms。请求握手与所有权验证重叠，未预启动 agent。
- 热启动到可输入 Codex 首屏：375 / 367 / 347ms。
- 远端刷新：268–340ms；read 约 294–304ms；常规 stop 约 300ms。
- send 约 790ms，仍不达标。Herdr 0.1.18 对新程序有 800ms 输出稳定等待，粘贴与 Enter 另有 200ms 间隔；不得用 `--force` 或返回排队回执冒充输入完成。
- attach/resume 原为约 560ms。隔离测量确认远端首屏生成约5ms，但回执与初始画面分包多出约230ms。初始画面合批（最多25ms、8ms静默窗口）后，隔离真实网络测量为306 / 307 / 342ms，完整 App 结果见上。
- 一次 resume 超时和随后 stop 超时仍需复核；不能因为正常样本通过而忽略。

性能测试当前总体仍为 FAIL，目标保持 active。最近完整日志：`/tmp/fennec-e2e-ops-display-v6.log`；冷启动记录：`/tmp/fennec-cold-v6.jsonl`。这些是当次测量，不是后续构建自动继承的结论。

## 仍需继续

### 冷路径实现与早期 staging 记录

`internal/launcher/cold_stream.go` 在 broker 完成所有权校验后，验证 runtime ID 与主机密钥快照，再通过已有的本次冷 SSH master 执行 `serve-initial`。使用非 PTY SSH channel，一次 exec 携带启动帧；本地终端信息随帧送入。`terminalstream.OfferLocal` 共用流式 UI 路径，`StartInitial` 预注册邮箱，避免早到回执丢失或重复发 launch。已补初始化帧、目标不匹配、主机密钥变化的测试。

暂存构建 `/tmp/fennec-cold-once.aBz2xM/Fennec.app`；试跑 `/tmp/fennec-cold-stream.jsonl` 为5451 / 3783 / 3789ms。签名 staging 再测 `/tmp/fennec-cold-stream-signed.jsonl` 为10268 / 4230 / 4310ms。最慢一例 `cold.ssh_handshake` 为8789ms、并行 broker 请求5692ms，远端准备只有十余毫秒。不得剔除这个样本后宣称达标。需要分解 DNS/TCP/KEX/auth 的时间，并在真实已安装路径复验。

还要把冷路径省往返的收益对齐普通 daemon 首次 channel 初始化，不能仅优化显式 `FENNEC_COLD_START` 测量分支。可研究在已完成所有权校验、远端通过显式 protocol 参数拒绝不兼容二进制的前提下，将首个 launch 随 channel 初始化送出；不能在校验完成前启动 agent。

- 压缩真实冷 TUI 路径剩余往返，消除超过5秒的样本；继续关注偶发重连异常。
- 解决 send 的实际完成时间，保留用户输入/对话框保护，不缩小验收边界。
- 旧显式项目同步、bulk transfer 和私有 project-launch 路径仍需独立审计；不能用常规会话路径的结果替它们宣称已收敛。
- 完成 GUI↔CLI 设置变更的双向可见性检查，补齐仍未实测的首次登录/审批交互。
