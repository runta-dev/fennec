# 干净环境 onboarding 审计：2026-11-11

最终用户验收尚未通过；本记录只描述本轮源码与隔离测试证据。
用户选择先完成开发，再安排干净 Mac/测试账号。当前本机 UTM 仅发现一台
已停止的 Windows VM，没有已准备好的干净 macOS 测试环境。

## 已发现并修复的开发环境依赖

1. `ensureControl` 原先只比较 build ID，App 移动后同一 binary 可能继续复用
   旧路径 daemon 和 LaunchAgent。现在同时检查实际执行路径及注册内容；
   旧路径/旧注册/无响应注册会在启动锁内被替换。兼容 symlink 不会误触发重启。
   回归覆盖移动路径、旧版本、旧协议缺执行路径、缺注册、转义路径及符号链接。
2. Swift 默认 deployment target 取决于构建主机。实际旧开发包的 UI/helper
   `minos=26.0`，已发布 0.2.11111055 的 UI/helper `minos=15.0`。
   现在固定编译 target 为 macOS 13.0，Info.plist 显式记录最低版本，包装
   校验检查所有 Mach-O 最低版本不高于 13.0；helper 缓存按 target 分隔。
   现有用户正在使用的 helper 未被替换，也未重置任何 Keychain 数据。

验证：`go test -race ./internal/cli ./internal/controlplane` 通过；macOS 13
Swift typecheck 通过；签名 staging 包 `/tmp/fennec-onboarding-os13/Fennec.app`
通过 `scripts/verify-macos-package.sh`。这不是 macOS 13 真实运行验收，也不是
已发布的新 DMG。日志：`/tmp/fennec-onboarding-os13-build.log`、
`/tmp/fennec-onboarding-os13-verify.log`。

## 卸载开发进度

已实现的底层组件：

- `internal/uninstall` 定义共用的确认与执行顺序：确认 → 停止自动恢复 →
  可选云端删除并确认完成 → 关闭开机启动 → 停止本机服务 → 恢复命令 →
  删除 Fennec 专属凭据 → 清理本机数据 → 删除自身 App。
- `internal/runta/DeleteOwnedShared` 对确认的同一 runtime 重新检查用户、owner、
  FENNEC_PURPOSE 和 revision；DELETE 接受不算完成，必须观察删除完成/404。
  不会用新 revision 自动重发 DELETE，也不会寻找并删除另一个 runtime。
- 本机计划仅枚举 Fennec 的目录及经过 bundle identifier 检查的 App；开发版
  归档和 helper 缓存限于当前 Fennec 仓库 build 目录，不删除源码或原生 Agent 凭据。
- 已实现 managed entry 恢复；如果其他包管理器已经更新了命令，则保留新命令。
  shell 清理只移除有唯一边界的 Fennec block，保留用户内容、权限和 dotfile symlink。
- 回归覆盖本机卸载不删除云端、错误确认零 mutation、云端失败时不清本机、
  删除接受后超时不算成功、归属/purpose/revision 变化拒绝删除、保留源码/native auth、
  外部命令更新和 shell 配置恢复。

`go test -race ./internal/uninstall ./internal/launcher ./internal/agent
./internal/runta ./internal/cli ./internal/controlplane` 通过，日志：
`/tmp/fennec-uninstall-first-batch.log`。所有删除测试均为临时目录/HTTP fixture。
没有执行任何真实云端删除或本机 Fennec 卸载。

必须继续完成：

- 共用 plan/execute/status 控制 API、`fen uninstall` 与安全 dry-run。
- 默认不选云端删除的危险 modal，明确绑定目标；CLI 显式确认参数。
- 持久化卸载意图、取消准备任务、阻止自动重建 runtime，未知结果恢复和重试。
- 独立自卸载执行器，确保删除自身后仍能完成清理；禁止误杀其他程序。
- 真实 login-item 注销、LaunchAgent 注销、Fennec Keychain 条目删除、WebKit/
  UserDefaults 清理，以及自定义 ZDOTDIR/XDG 的安装记录与发现。
- 最终签名 DMG、干净 Mac/新用户/系统重启及同事独立使用验收。

## 后续：共用卸载入口、恢复与移除 App 后的命令行为

以下为开发实现及回归证据，尚未做真实用户自卸载或删除真实云机器：

- 已接入设置页卸载 modal 与 `fen uninstall` 辅助命令，共用 Go 预览、确认
  和 worker 逻辑。默认只清理本机；可选云端删除必须输入具体 runtime ID。
  CLI 支持 `--dry-run`、`--yes`、`--delete-cloud --confirm-runtime <id>`。
- 预览绑定本机安装和源码 hash；执行前再次校验。恢复预览只能重新确认同一
  云端 ID/owner，不能转向替代 runtime。新 revision 必须经过新的预览和确认。
- 持久卸载标记位于 `.local/state/fennec-uninstall/active.json`，晚于 App
  清理才移除。它独立于普通设置目录，阻止失败后/重启后自动创建云机器。
  准备任务先取消并排空，普通 mutation 被暂停；重新登录和卸载恢复仍可使用。
- worker 从私有临时目录运行，签名 binary 字节与已确认 App 相同。清理范围
  包括当前注册的 bundle；进程匹配限制当前用户、精确 executable 路径，保留
  当前 CLI caller 和 worker。完成回执在本机清理及防重建标记移除之后发布。
- 云端失败、进程中断、凭据过期、旧预览、错误确认均保留恢复路径；不自动
  重发删除操作。UI 提供重新登录/重试；默认不展示内部状态码。
- App 消失后的 managed entry/shim 可运行已解析的原始 Agent，并明确告知
  当前在本机执行。参数、工作目录和退出码保留；App 仍存在时的云端失败绝不
  触发本机 fallback。原始 executable 发现会跳过其他 Fennec shim，防止递归。
- 设置页是主入口；Account 的独立“删除云电脑”入口仍需实现，不能把卸载弹窗
  中的可选项当作该独立功能的验收。

本轮证据：

- `/tmp/fennec-uninstall-review-tests.log`：launcher、agent、CLI、controlplane、
  uninstall、Runta 客户端 race 测试通过。
- `/tmp/fennec-uninstall-review-ui.log`：UI 检查及危险确认回归通过；包括默认不选
  云端删除、错误 runtime ID 不提交、过期 UI 回复不覆盖新选择、失败不自动重试。
- `/tmp/fennec-uninstall-review-vet.log`：Go vet 通过。
- 已成功跑过签名 staging 包的 `fen uninstall --dry-run`，仅生成预览，未调用
  worker。没有触发本机 Keychain 删除、服务注销、App 删除或任何真实云端 DELETE。

仍需实际验收：独立云端删除入口、完整自卸载/崩溃恢复、系统注销/重启、权限
与多副本的 SMAppService 行为、只拖入废纸篓后的后台注销、自定义 shell 安装
位置的持久记录、干净 Mac/正式 DMG/真实账号/独立同事完整流程。不得将上述
fixture、源码与 staging 结果标为最终用户验收完成。
