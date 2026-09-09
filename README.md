# vpsctl

[![CI](https://github.com/Xichun123/vpsctl/actions/workflows/ci.yml/badge.svg)](https://github.com/Xichun123/vpsctl/actions/workflows/ci.yml)
[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-blue.svg)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![Agent Skill](https://img.shields.io/badge/Agent%20Skill-compatible-111827.svg)](skills/vpsctl/SKILL.md)

`vpsctl` 是面向 AI Agent 的 SSH/VPS CLI。0.4.0 使用 Go 标准库和系统 OpenSSH，所有命令（包括帮助和错误）只输出 JSON，不提供交互式提示。仅支持密钥或 `ssh-agent` 认证。

## 要求与安装

- 本地 Linux 或 macOS；运行时使用系统 `ssh`，密钥管理还需 `ssh-keygen`。
- 远端为 Linux，具备 POSIX `sh` 和 GNU coreutils；密钥管理需要 `awk`。
- 后台任务还需 Linux `/proc`、`nohup`、`base64` 和 util-linux 的 `setsid`。
- 递归或续传操作要求本地和远端均安装 **rsync 3+**；macOS 自带旧版 rsync 不满足要求。
- 连接前必须通过可信渠道核实服务器指纹，并预先配置 `known_hosts`；未知或变化的主机密钥会被拒绝。
- **CLI 与 Skill 分开安装**：安装 CLI 不会安装 Skill，安装 Skill 也不会安装 CLI。

发布 `vX.Y.Z` 标签后，GitHub Release 提供校验过的二进制。默认安装到 `~/.local/bin/vpsctl`，不修改 shell、SSH 或 Skill 配置：

```bash
curl -fsSL https://github.com/Xichun123/vpsctl/releases/latest/download/install.sh | bash
vpsctl --version
```

固定版本，或改安装目录：

```bash
curl -fsSL https://github.com/Xichun123/vpsctl/releases/download/v0.4.0/install.sh | bash -s -- v0.4.0
VPSCTL_INSTALL_DIR="$HOME/.local/bin" bash scripts/install.sh v0.4.0
```

安装脚本会解析一次 latest、校验 SHA-256，失败时不覆盖已有 CLI。确保 `~/.local/bin` 同时出现在终端和 Agent 的 `PATH` 中。

源码构建需要 Go 1.26.0 或更新版本，无第三方 Go 模块：

```bash
git clone https://github.com/Xichun123/vpsctl.git
cd vpsctl
go build -o dist/vpsctl .
./dist/vpsctl --version
```

Skill 仍放在仓库的 `skills/vpsctl/`。使用 [`skills`](https://www.npmjs.com/package/skills) CLI 为 Universal 和 Pi 全局安装：

```bash
skills add Xichun123/vpsctl --skill vpsctl -g -a universal -a pi -y
```

从本地仓库验证发现并安装：

```bash
skills add . --list
skills add . --skill vpsctl -g -a universal -a pi -y
```

## 快速开始

下列主机、路径及服务均为示例；修改前确认真实目标、敏感路径和影响。

```bash
# 本地发现与配置解析，不等于连通性或认证检查
vpsctl host list
vpsctl host find web
vpsctl host show prod-web-01

# 修改本地 SSH config；不自动信任服务器密钥
vpsctl host add --alias prod-web-01 --hostname 192.0.2.10 \
  --user deploy --identity ~/.ssh/id_ed25519

# 只读与授权修改都用 exec
vpsctl exec prod-web-01 'hostname && uptime'
vpsctl exec prod-web-01 --cwd /opt/my-app \
  'docker compose pull && docker compose up -d'
vpsctl exec prod-web-01 --cwd /opt/my-app 'docker compose ps'

# POSIX sh 脚本通过 stdin 或文件传入
vpsctl exec prod-web-01 --cwd /opt/my-app --script-file ./deploy.sh
vpsctl exec prod-web-01 --stdin < check.sh

# 长任务：保存返回的 data.job_id，后续按该 ID 查询，勿重复提交
vpsctl exec prod-web-01 --cwd /opt/my-app --detach --script-file ./deploy.sh
vpsctl job status prod-web-01 <job-id>
vpsctl job output prod-web-01 <job-id> --cursor 0 --limit 65536

# 单文件传输默认不覆盖；覆盖必须显式授权并传 --overwrite
vpsctl --timeout 30m upload prod-web-01 ./release.tar.gz /tmp/release.tar.gz
vpsctl download prod-web-01 /var/log/app.log ./app.log
vpsctl upload prod-web-01 ./dist /var/www/app --recursive --overwrite
vpsctl transfer old-host /data/export.tar new-host /data/export.tar

# 明确列出批量操作的全部目标
vpsctl cluster --hosts prod-web-01,prod-web-02 --parallel 2 'uptime'

# 本地回环端口转发；保存 data.id 用于查询/停止
vpsctl tunnel start prod-db-01 --local-port 15432 --remote-host 127.0.0.1 --remote-port 5432
vpsctl tunnel list
vpsctl tunnel status prod-db-01 --id <tunnel-id>
vpsctl tunnel stop prod-db-01 --id <tunnel-id>
```

## JSON 与执行语义

所有响应使用统一结构，例如远端命令正常结束：

```json
{"status":"completed","exit_code":0,"stdout":"ok\n","stderr":"","error":null,"truncated":false}
```

- `status` 为 `completed`、`running`、`failed` 或 `unknown`；`completed` 只表示完成，仍需检查 `exit_code`。
- 成功要求 `error == null` 且 `exit_code == 0`；`running` 仅表示后台任务已启动，不代表任务完成。
- `exit_code: null` 表示没有可用的完成退出码。CLI 进程对成功或已启动任务返回 0，其余返回 1；远端实际退出码在 JSON 中。
- `stdout`、`stderr` 始终存在；`data` 包含命令特有结果。旧版 `success` 字段不再使用。
- 全局选项必须放在命令前：`--ssh-config FILE`、`--timeout 30s`、`--max-output 1048576`。超时采用 Go duration 格式，默认 0（无限本地等待）；输出默认每流最多 1 MiB，截断时 `truncated: true`。
- **本地超时、取消或 SSH 断线不保证远端停止。** `unknown` 不是安全重试许可；先检查目标状态或使用已返回的任务 ID 对账，禁止盲目重复修改。
- 脚本运行于 POSIX `sh`；需要其他语言时显式调用其解释器。`--cwd` 指定远端绝对目录。普通执行先将脚本完整写入远端 `0600` 临时文件，再运行；子命令默认 stdin 为 `/dev/null`，需要输入时在脚本内使用重定向或 heredoc。临时脚本正常退出时删除，强制终止或主机故障可能留下文件。
- 批量命令的全部主机 stdout/stderr 合计最多保留 8 MiB，各主机保留独立的结果与截断标记。

## 后台任务与文件传输

`exec --detach` 将脚本、合并日志和完成记录保存在远端账号的 `~/.local/state/vpsctl/jobs/<job-id>/`。后台任务可跨 SSH 断线继续运行，但不提供重启恢复、自动重试、取消或自动清理。缺失完成记录不能视作成功；日志不轮转、不限总大小，需管理磁盘使用。

`job output` 的游标和上限均为**字节数**。读取 `data.output_base64` 获取无损原始字节，下一次使用 `data.next_cursor`；`stdout` 只是 UTF-8 展示文本，可能替换无效字节。输出读取完成不代表任务已结束，仍需 `job status`。

单文件上传、下载和服务器间传输使用 SHA-256 校验、私有临时文件及目标端原子提交；默认拒绝替换已有目标，`--overwrite` 才允许替换。新文件模式为 `0600`，不会自动继承源文件的可执行权限。服务器间文件通过本机流式中转，不保存本地副本，也不提供直连或混合模式。

`--recursive` / `--resume` 使用 rsync，目录复制其**内容**。默认跳过已有文件，`--overwrite` 才更新；该模式不是整个目录的原子事务，失败可能已修改部分文件。需要原子部署时使用独立发布目录，再经明确授权切换入口。

## SSH 配置与安全

默认使用标准 `~/.ssh/config`，支持 OpenSSH `ProxyJump`。主机清单只枚举字面别名，可读取静态 `Include`，不评估 `Match`，不证明连接成功。可在 Host 前写 `# vpsctl-labels: web production` 供 `host find` 搜索。

```ssh-config
Host prod-web-01
    HostName 192.0.2.10
    User deploy
    IdentityFile ~/.ssh/id_ed25519
    Port 22

Host internal-web
    HostName 10.0.1.10
    User deploy
    IdentityFile ~/.ssh/id_ed25519
    ProxyJump bastion
```

- SSH config 是可信的本地代码，`ProxyCommand` / `Match exec` 等可执行命令；不要读取不可信配置。`host show` 与 `key verify` 拒绝含 `Match` 的配置。自动主机编辑只支持简单配置，遇到 `Include`、`Match` 或含不支持指令的目标块会拒绝修改。
- `key verify` 暂不支持 `ProxyJump` 配置：为避免隔离密钥时错误重写跳板认证，会明确返回 `UNSUPPORTED_CONFIG`；普通执行和传输仍支持跳板。
- 强制非交互式公钥认证与严格主机验证，无密码或口令提示。加密私钥须事先在 `ssh-agent` 中解锁；跳板机也必须预配非交互式认证和已验证的主机密钥。
- 不要使用自动接受未知密钥或关闭验证的方式消除错误；`ssh-keyscan` 获取的内容也必须通过独立可信渠道核实后才能信任。
- 生产修改、删除、覆盖、密钥写入前确认目标和影响；保护 `.env`、私钥、证书和生产配置，未经明确批准不得覆盖、删除或重建。
- 不在命令、脚本、日志或报告中暴露 token、密码或私钥。stdin、临时文件权限和 base64 不是秘密保险库；后台任务会持久保存脚本与日志。
- 工具创建的临时文件使用 `0600`，私有目录使用 `0700`；这些权限不能隔离同一账号下的其他进程。
- 修改后执行最小范围的只读验证，失败也可能已部分生效；不应未经验证就宣告成功。

## 命令与开发

完整命令参数和限制见 [`skills/vpsctl/references/commands.md`](skills/vpsctl/references/commands.md)。Skill 工作流见 [`skills/vpsctl/SKILL.md`](skills/vpsctl/SKILL.md)。本版是破坏性变更，不兼容旧版命令或 JSON 合同；迁移摘要见 [`CHANGELOG.md`](CHANGELOG.md)。

```bash
go test -race ./...
go vet ./...
go build -o dist/vpsctl .
scripts/check-skill-package.sh
```

默认测试不访问真实 VPS。Skill 检查需要 `skills` CLI，验证发现和 Universal/Pi 隔离安装；不会安装 CLI 二进制。

提交问题或 PR 前请阅读 [`CONTRIBUTING.md`](CONTRIBUTING.md)。安全问题请遵循 [`SECURITY.md`](SECURITY.md)，不要在公开 Issue 中提交凭据或真实基础设施信息。

## 许可证

本项目使用 [MIT License](LICENSE)。
