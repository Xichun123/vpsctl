# vpsctl

[![CI](https://github.com/Xichun123/vpsctl/actions/workflows/ci.yml/badge.svg)](https://github.com/Xichun123/vpsctl/actions/workflows/ci.yml)
[![Python 3.9+](https://img.shields.io/badge/python-3.9%2B-blue.svg)](https://www.python.org/downloads/)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![Agent Skill](https://img.shields.io/badge/Agent%20Skill-compatible-111827.svg)](skills/vpsctl/SKILL.md)

`vpsctl` 是一个面向 AI Agent 和运维人员的统一 SSH/VPS 命令行工具。它管理标准 `~/.ssh/config` 主机、执行命令与传输、维护项目部署档案，并持久保存远端状态快照和变更记录。

AI Agent 首次建档时可显式探测服务器建立基线；后续通过本地 `vpsctl context` 读取档案与变更日志，避免每次重新连接和探索服务器。仓库同时提供符合 [Agent Skills specification](https://agentskills.io/specification) 的 Skill 包。

## 功能

- 使用 `~/.ssh/config` 中的别名管理主机
- 执行单条命令、stdin 脚本或本地脚本文件
- 上传、下载、递归传输和断点续传
- 服务器间直连、流式或混合传输
- 多服务器批量并发执行与健康检查
- SSH Tunnel 本地端口转发
- 密码认证长连接守护进程与自动重连
- ProxyJump 跳板机
- SSH 密钥添加、验证、回滚、部署和认证迁移
- 旧 JSON 配置迁移与 SSH config 修复
- 项目、部署规则、操作命令、保护路径等长期档案
- 主机和项目的只读远端探测与历史快照
- 默认紧凑、无网络、无 TTL 的 `tracked` Agent 上下文
- `vpsctl apply` 自动执行修改并记录摘要、结果与命令哈希
- 上传等操作可通过 `vpsctl change add` 补记
- 显式刷新失败时回退到最后一次成功快照
- JSON 结果输出，便于 Agent 稳定解析

## 安装

要求 Python 3.9+ 和 OpenSSH 客户端；Python 安装过程会自动安装 `paramiko`。

推荐使用 [`uv`](https://docs.astral.sh/uv/) 从 GitHub 隔离安装 CLI：

```bash
uv tool install 'git+https://github.com/Xichun123/vpsctl.git'
```

没有 `uv` 时也可以使用 pip：

```bash
python -m pip install 'git+https://github.com/Xichun123/vpsctl.git'
```

从本地检出安装：

```bash
git clone https://github.com/Xichun123/vpsctl.git
cd vpsctl
uv tool install .
```

确认安装：

```bash
vpsctl --version
vpsctl --help
```

### 安装 Agent Skill

Skill 安装不会安装 Python CLI；请先完成上面的 CLI 安装。使用 [`skills`](https://www.npmjs.com/package/skills) CLI 为 Universal 和 Pi 全局安装：

```bash
skills add Xichun123/vpsctl --skill vpsctl -g -a universal -a pi -y
```

从本地仓库验证发现并安装：

```bash
skills add . --list
skills add . --skill vpsctl -g -a universal -a pi -y
```

## 快速开始

```bash
# 查看 CLI
vpsctl --help
vpsctl --version

# 主机资产
vpsctl list
vpsctl find web
vpsctl host list --environment production
vpsctl host create \
  --alias prod-web-01 \
  --host 192.0.2.10 \
  --user root \
  --key ~/.ssh/id_ed25519 \
  --environment production \
  --tags web nginx

# 建立项目档案
vpsctl project add my-app \
  --host prod-web-01 \
  --path /opt/my-app \
  --runtime docker-compose \
  --compose-file compose.yaml \
  --domain app.example.com \
  --deploy-command 'git pull && docker compose up -d --build' \
  --restart-command 'docker compose restart' \
  --log-command 'docker compose logs --tail=200' \
  --protect .env \
  --tag production

# 首次建档后探测一次，建立远端基线
vpsctl refresh project my-app

# 日常任务直接读取本地紧凑上下文，不连接 VPS
vpsctl context --project my-app

# 只读检查使用 exec
vpsctl exec prod-web-01 "hostname && uptime"

# 修改操作使用 apply，成功或失败都会写入变更日志
vpsctl apply my-app --kind deploy --summary '部署新版本' \
  'docker compose pull && docker compose up -d'

# 多行修改脚本同样由 apply 自动记录
cat deploy.sh | vpsctl apply my-app --stdin \
  --kind deploy --summary '执行部署脚本'

# 查询变更记录
vpsctl change list --project my-app

# 文件传输
vpsctl upload prod-web-01 ./dist /var/www/app --recursive
vpsctl download prod-web-01 /var/log/app.log ./app.log --resume
vpsctl transfer old-host /data new-host /data --mode hybrid

# 批量执行
vpsctl cluster "uptime" --parallel
vpsctl cluster "systemctl is-active nginx" --tags web,nginx --parallel --health-check

# Tunnel 与守护进程
vpsctl tunnel start prod-db-01 --remote-port 5432
vpsctl tunnel list
vpsctl daemon status prod-web-01
```

每个叶子命令均可继续查看原运行时的完整参数：

```bash
vpsctl exec --help
vpsctl host create --help
vpsctl tunnel start --help
vpsctl key add --help
```

## 命令映射

| vpsctl 命令 | 能力 |
|---|---|
| `host list/find/create/update/delete/export` | SSH 主机配置管理 |
| `exec` | 不自动记录的只读命令执行 |
| `apply` | 执行项目修改并自动记录变更 |
| `change add/list` | 补记或查询变更日志 |
| `upload` / `download` | SFTP/原生 SSH 文件传输 |
| `transfer` | 服务器间传输 |
| `cluster` | 批量执行与健康检查 |
| `tunnel` | 本地端口转发 |
| `daemon` | 长连接进程管理 |
| `key add/verify/rollback/deploy/migrate` | 密钥生命周期管理 |
| `config migrate/annotate/fix` | 配置迁移与修复 |
| `inventory refresh` | 旧版系统信息采集 |
| `project add/update/show/list/delete/export` | 项目部署档案 |
| `refresh host/project` | 首次建档或排障时显式校准基线 |
| `context --host/--project` | 默认本地、紧凑的 Agent 上下文 |

## 配置格式

默认读取标准的 `~/.ssh/config`，并在 Host 前的注释中保存非 SSH 元数据：

```ssh-config
# ===== prod-web-01 =====
# description: 生产 Web 服务器
# environment: production
# tags: web,nginx
# location: example-region
Host prod-web-01
    HostName 192.0.2.10
    User root
    IdentityFile ~/.ssh/id_ed25519
    Port 22
```

项目档案和状态快照默认保存在 `~/.vpsctl/state.db`。可通过 `VPSCTL_DB` 指定其他数据库路径：

```bash
VPSCTL_DB=~/.vpsctl/work.db vpsctl project list
```

支持标准 `ProxyJump`：

```ssh-config
Host internal-web
    HostName 10.0.1.10
    User deploy
    IdentityFile ~/.ssh/id_ed25519
    ProxyJump bastion
```

## 项目档案与最新状态

项目档案保存以下静态信息：

- SSH 主机别名和远程绝对路径
- 仓库、分支、runtime、systemd service、Compose 文件
- 域名、标签、备注和需要保护的文件
- 部署、重启、日志和健康检查说明

```bash
vpsctl project list
vpsctl project show my-app
vpsctl project update my-app --service my-app.service --protect config/prod.yaml
vpsctl project export --output projects.json
```

只读刷新会采集：

- 主机：系统、内核、CPU、内存、根磁盘、地址、监听端口、Docker 容器、失败的 systemd unit
- 项目：目录是否存在、顶层文件、Git remote/branch/commit/status、Compose 服务和已配置 systemd service 状态

```bash
# 首次建档或怀疑外部漂移时显式校准
vpsctl refresh project my-app
vpsctl refresh host prod-web-01

# 日常读取：本地完成，不发起 SSH
vpsctl context --project my-app

# 需要完整基线时才使用
vpsctl context --project my-app --full

# 只有明确需要即时远端校准时才组合使用
vpsctl context --project my-app --refresh --timeout 90
```

`context` 状态：

| 状态 | 含义 |
|---|---|
| `tracked` | 使用首次基线与之后的 vpsctl 变更日志；默认状态 |
| `fresh` | 显式设置 `--max-age` 且基线仍在时间范围内 |
| `stale` | 仅在显式设置 `--max-age` 后，基线超过该时间 |
| `missing` | 从未成功建立基线 |
| `refresh_failed` | 最近显式刷新失败，同时保留最后成功基线 |

刷新只执行内置读取脚本，**不会**自动执行项目档案中的部署、重启或日志命令。日常修改应使用：

```bash
vpsctl apply my-app --kind config --summary '更新应用配置' --script-file ./update.sh
vpsctl change add my-app --kind upload --summary '上传前端构建产物'
```

`apply` 不保存命令原文，只保存变更摘要、执行结果、操作类型和 payload SHA-256。

## 安全说明

- 优先使用密钥和 `ssh-agent`，不要把真实私钥提交到项目中。
- 兼容运行时支持把密码写在 SSH config 注释中，但不推荐这样做；后续应改接系统 Keychain/Secret Service。
- 日常 `context` 不联网；`context --refresh` 与 `refresh` 只运行内置读取命令。
- 远端修改使用 `apply` 自动留痕；命令原文不入库，避免记录其中可能存在的敏感值。
- Agent 执行项目前必须遵守 `protected_paths`，并在修改生产环境前确认目标和影响。
- 不要把 token、密码或私钥直接写入项目档案中的命令和备注；状态数据库虽限制为当前用户读取，但不是密钥保险库。
- `config annotate`、`config fix`、`inventory refresh`、主机增删改和密钥命令会修改本地或远程状态，执行前确认目标并备份配置。
- 上下文会显示认证类型，但不会输出 SSH config 注释中的密码。
- 只读复杂脚本使用 `exec --stdin/--script-file`；修改脚本使用 `apply --stdin/--script-file`。

## Agent Skill

标准 Skill 包位于 [`skills/vpsctl/`](skills/vpsctl/)，名称与父目录一致。主文件只保留工作流和安全规则，详细命令按需读取 [`references/commands.md`](skills/vpsctl/references/commands.md)。

自动检查会验证：

- `skills add . --list` 能发现且只发现 `vpsctl`。
- Universal 和 Pi 隔离安装均成功。
- `SKILL.md` 与所有 `references/` 伴随文件都被完整复制。

## 开发与测试

```bash
python -m venv .venv
source .venv/bin/activate
python -m pip install -e .

PYTHONPATH=src python -m unittest discover -s tests -v
PYTHONPATH=src python -m compileall -q src tests
python -m pip wheel . --no-deps --wheel-dir dist
scripts/check-skill-package.sh
```

目录结构：

```text
.github/                       # CI、Dependabot、Issue 与 PR 模板
skills/vpsctl/                 # Agent Skills 标准包
scripts/check-skill-package.sh # skills CLI 发现与隔离安装测试
src/vpsctl/cli.py              # 稳定的统一命令分发层
src/vpsctl/store.py            # SQLite 项目档案与快照
src/vpsctl/discovery.py        # 只读远端探测
src/vpsctl/context.py          # 紧凑 Agent 上下文
src/vpsctl/apply_cli.py        # 修改执行与变更日志
src/vpsctl/_runtime/           # SSH 运行时
```

## 参与贡献

提交问题或 PR 前请阅读 [`CONTRIBUTING.md`](CONTRIBUTING.md)。安全问题请遵循 [`SECURITY.md`](SECURITY.md)，不要在公开 Issue 中提交凭据或真实基础设施信息。

## 许可证

本项目使用 [MIT License](LICENSE)。
