# Rollin V1 部署手册（GitHub Actions → 服务器）

面向值班/运维的部署与运行手册。**部署全部通过 GitHub Actions 自动完成**，本手册说明
一次性准备工作和日常运维动作。

## 0. 一页速览

| 关注点 | 做法 |
| --- | --- |
| 触发部署 | push 到 `main`（改动命中对应目录即自动触发），或手动 `workflow_dispatch` |
| 前端 | Actions 构建 Vite 产物 → `scp` 到服务器 → 解包到版本目录 → 原子切换 `current` 符号链接 → `nginx reload`。**不使用 Docker** |
| 后端 | Actions 构建镜像 → 推 `ghcr.io/<owner>/rollin-backend` → SSH 在服务器上 `docker pull` + `docker run`。**不使用 docker compose** |
| MySQL / Redis | 外部服务，不由流水线或本仓任何脚本启动 |
| 数据库迁移 | 后端流水线内自动执行（换容器之前），迁移前自动 `mysqldump` 备份 |
| 失败处理 | 后端健康门禁不过自动回滚到上一个镜像；前端 `nginx -t` 不过自动回滚符号链接 |
| 部署通知 | 部署开始/结束各推一条飞书群卡片，含提交号、提交说明、触发者、用时、失败阶段（见 §5.5） |

本目录其余文件：

| 文件 | 作用 |
| --- | --- |
| `nginx.conf.example` | 宿主机 nginx 完整配置（TLS + 双域名白名单 + 前端静态交付） |
| `scripts/backend-deploy.sh` | 服务器端后端部署脚本（备份 → pull → migrate → 换容器 → 健康门禁/回滚） |
| `scripts/frontend-deploy.sh` | 服务器端前端部署脚本（解包 → 原子切换 → nginx reload → 清理旧版本） |
| `../.github/workflows/deploy-backend.yml` | 后端流水线 |
| `../.github/workflows/deploy-frontend.yml` | 前端流水线 |
| `../.github/actions/prepare-ssh/action.yml` | 两条流水线共用的 SSH 准备步骤 |
| `../.github/actions/notify-feishu/` | 两条流水线共用的部署通知（飞书卡片渲染 + 签名 + 发送，见 §5.5） |
| `../docker-compose.dev.yml` | **仅本地开发/验收**，自带 MySQL/Redis 容器；生产不使用 |
| `../docker-compose.legacy.yml` | 已废弃的旧部署方式，留档用，勿在生产使用 |

## 1. 拓扑

```
                     ┌──────────────────────── 服务器 ────────────────────────┐
浏览器 / 外部系统 ───►│ 宿主机 nginx（443 TLS，deploy/nginx.conf.example）      │
                     │   admin.xxx.xxx                                        │
                     │     /            → /var/www/rollin/current（磁盘静态） │
                     │     /api/*       → rollin_backend 127.0.0.1:8080       │
                     │   t.xxx.xxx                                            │
                     │     /o/{token}   → /var/www/rollin/current（fallback）  │
                     │     /api/public/*→ rollin_backend 127.0.0.1:8080       │
                     │     其余 /api/*  → 404（白名单隔离）                    │
                     │     其余路径     → 404                                 │
                     │                                                        │
                     │   /var/www/rollin/current ──符号链接──►                 │
                     │       /opt/rollin/releases/sha-abc1234/  （前端产物）   │
                     │                                                        │
                     │   rollin-backend 容器 :8080（HEALTHCHECK /healthz）     │
                     └───────────┬────────────────────────┬───────────────────┘
                                 ▼                        ▼
                        外部 MySQL 8.4             外部 Redis 7
                     （业务数据/迁移账本）      （Session/锁/限流，可丢）
```

要点：

- **域名隔离完全由宿主机 nginx 白名单实现**。后端 chi 只监听单端口，直连
  `127.0.0.1:8080` 就能访问全部 `/api/*`，因此 8080 **绝不能**对公网开放，
  防火墙/安全组也不得放行。
- MySQL / Redis 是外部服务，本仓任何部署脚本都不会启动数据库容器。
- 迁移不随服务启动自动执行（服务启动有只读 schema 门禁），由后端流水线显式调用
  `migrate up`，且**先迁移、后换容器**。
- `/healthz` 在 nginx 上显式 404，只允许在服务器本机访问。

## 2. 服务器一次性准备

以下操作只需做一次。假设用非 root 用户 `deploy` 承接部署。

### 2.1 基础软件

```bash
# Docker（Debian/Ubuntu 示例）
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker deploy      # 让 deploy 用户免 sudo 使用 docker

# nginx（需 >= 1.25.1，配置里用了 `http2 on;` 新语法）
sudo apt-get update && sudo apt-get install -y nginx
nginx -v
```

### 2.2 目录与所有权

```bash
# 前端版本目录（deploy 用户需要可写）
sudo mkdir -p /opt/rollin/releases /opt/rollin/backups
sudo chown -R deploy:deploy /opt/rollin

# nginx 站点根目录（current 是指向 releases 的符号链接）
sudo mkdir -p /var/www/rollin
sudo chown -R deploy:deploy /var/www/rollin
```

### 2.3 后端环境变量文件

服务器上**唯一存放真实凭证**的地方，权限必须 600，绝不入库、绝不进流水线。

```bash
sudo mkdir -p /etc/rollin
sudo cp /path/to/rollin/rollin-backend/.env.example /etc/rollin/backend.env
sudo chown deploy:deploy /etc/rollin/backend.env
sudo chmod 600 /etc/rollin/backend.env
sudo -e /etc/rollin/backend.env      # 或 vim
```

必须逐项确认的值：

| 变量 | 说明 |
| --- | --- |
| `MYSQL_HOST` / `MYSQL_PORT` / `MYSQL_DATABASE` / `MYSQL_USER` / `MYSQL_PASSWORD` | 外部 MySQL 地址与凭证。**不能填 127.0.0.1**（容器内 127.0.0.1 指向容器自身） |
| `REDIS_HOST` / `REDIS_PORT` / `REDIS_PASSWORD` | 外部 Redis 地址与凭证，同上 |
| `CANDIDATE_BASE_URL` | `https://t.<你的域名>` |
| `ADMIN_BASE_URL` | `https://admin.<你的域名>` |
| `SMTP_ENC_KEY` | `openssl rand -base64 32` 生成。**轮换会使已存 SMTP 密码无法解密，需重新录入** |
| `SUPER_ADMIN_EMAIL` / `SUPER_ADMIN_INITIAL_PASSWORD` | 首次启动幂等创建超管；仅在 `platform_admin` 空表时生效，永不改密 |
| `COOKIE_SECURE` | https 生产环境必须为 `true` |
| `CSRF_ALLOWED_ORIGINS` | 一般留空（请求 Host 即为白名单）。若两个域名需要互相跳转提交，把对方 host 加进来 |

> `docker run --env-file` 只认 `KEY=VALUE` 行，注释行可以保留。文件里的值会被原样注入容器，
> 不要加引号。

### 2.4 nginx 配置

```bash
sudo cp deploy/nginx.conf.example /etc/nginx/conf.d/rollin.conf
sudo vim /etc/nginx/conf.d/rollin.conf   # 替换 t.example.edu.cn / admin.example.edu.cn 与证书路径
sudo nginx -t && sudo systemctl reload nginx
```

TLS 证书（certbot）：

```bash
sudo apt-get install -y certbot python3-certbot-nginx
sudo certbot --nginx -d admin.example.edu.cn -d t.example.edu.cn
```

### 2.5 部署用户的提权（可选）

`frontend-deploy.sh` 需要写 `/var/www/rollin`、执行 `nginx -t` 与 `systemctl reload nginx`。
两种做法任选其一：

- **推荐**：按 §2.2 把 `/opt/rollin`、`/var/www/rollin` 的所有权给 `deploy`，
  并给最小粒度 sudo：

  ```bash
  sudo tee /etc/sudoers.d/rollin-deploy >/dev/null <<'EOF'
  deploy ALL=(root) NOPASSWD: /usr/sbin/nginx -t, /usr/bin/systemctl reload nginx
  EOF
  sudo chmod 440 /etc/sudoers.d/rollin-deploy
  ```

  然后在仓库变量里设 `DEPLOY_SUDO=sudo`（否则脚本不带 sudo 调用 `nginx -t`）。

- 或者：直接以 root 身份配置 SSH 部署（简单但权限过大，不推荐）。

### 2.6 SSH 密钥

在**本地**生成一对专用部署密钥，公钥追加到服务器的 `~/.ssh/authorized_keys`：

```bash
ssh-keygen -t ed25519 -C 'rollin-deploy' -f ~/.ssh/rollin_deploy -N ''
ssh-copy-id -i ~/.ssh/rollin_deploy.pub deploy@<server>   # 或手动追加到 authorized_keys
ssh -i ~/.ssh/rollin_deploy deploy@<server> 'docker info >/dev/null && echo ok'
```

私钥全文（`~/.ssh/rollin_deploy`，含首尾行）填进 §3 的 `SSH_KEY` secret。

同时记录服务器主机公钥，填进 `SSH_KNOWN_HOSTS`（避免每次用 `ssh-keyscan` 现场取）：

```bash
ssh-keyscan -p 22 -H <server> 2>/dev/null
```

## 3. GitHub 仓库配置

位置：仓库 **Settings → Secrets and variables → Actions**。

### 3.1 Secrets（必需）

| 名称 | 说明 |
| --- | --- |
| `SSH_HOST` | 服务器地址或 IP |
| `SSH_USER` | 部署用户（如 `deploy`） |
| `SSH_KEY` | 部署私钥全文 |
| `SSH_PORT` | SSH 端口，默认 22（可省略） |
| `SSH_KNOWN_HOSTS` | 服务器主机公钥，见 §2.6（建议配置；省略则流水线用 `ssh-keyscan` 现场获取） |
| `GHCR_TOKEN` | 可选。ghcr.io 读取令牌（classic PAT，勾 `read:packages`）。**不配**时自动回退用本次运行的 `GITHUB_TOKEN`，适用于镜像包与仓库关联的情况 |
| `FEISHU_WEBHOOK` | 可选。飞书群机器人 webhook 地址。**不配则完全跳过部署通知**（见 §5.5） |
| `FEISHU_WEBHOOK_SECRET` | 可选。飞书机器人开启「签名校验」时填对应密钥；未开启则留空 |

### 3.2 Variables（可选，仅用于冒烟检查与个性化）

| 名称 | 说明 |
| --- | --- |
| `ADMIN_BASE_URL` | 如 `https://admin.example.edu.cn`。配置后流水线会做 200/404 冒烟检查；不配则跳过 |
| `CANDIDATE_BASE_URL` | 如 `https://t.example.edu.cn`。用于验证白名单隔离 |
| `DEPLOY_SUDO` | 需要提权时填 `sudo`，见 §2.5 |
| `VITE_API_BASE` | 前端 API 基地址。**双域名部署请留空**（同源，各域名 nginx 分别反代） |
| `BACKUP_KEEP` | 保留最近几个数据库备份，默认 7 |
| `BACKEND_HEALTH_TIMEOUT` | 后端健康等待秒数，默认 90 |

> 冒烟检查意味着服务器必须能从公网被 Runner 访问到 443 端口。若服务器只在内网，
> 请不要配置 `ADMIN_BASE_URL` / `CANDIDATE_BASE_URL`，改为手动验收（§6）。

### 3.3 镜像仓库可见性

首次成功推送后，到 GitHub 的 **Packages → rollin-backend → Package settings**：

- 想省去服务器上的登录：把可见性设为 **Public**，此后 `GHCR_TOKEN` 可以完全不配。
- 保持 **Private**：必须配 `GHCR_TOKEN`（`GITHUB_TOKEN` 仅在包与仓库关联时可用）。

## 4. 首次部署

1. **先把仓库推到 GitHub**（当前本目录还不是 git 仓库）：

   ```bash
   cd <仓库根>
   git init -b main
   git add .
   git commit -m "chore: 初始化仓库"
   git remote add origin git@github.com:<owner>/<repo>.git
   git push -u origin main
   ```

   `.gitignore` 已排除 `.env`、`node_modules/`、`dist/`，不会误提交凭证与产物。
   **推送前请确认 `rollin-backend/.env` 不在待提交列表里。**

2. **完成 §2 服务器准备与 §3 仓库配置。**

3. **首次建库**：外部 MySQL 上先建库建账号（或依赖 `MYSQL_AUTO_CREATE_DATABASE=true`）。
   空库首建不需要备份文件，流水线会自动跳过 `--backup-path`。

4. **触发流水线**：手动跑一次 `Deploy Backend`，等它成功（迁移 + 容器起来 + 健康检查通过）。
   再手动跑 `Deploy Frontend`。

5. **验收**：见 §6。

## 5. 日常发版与回滚

### 5.1 发版

直接 push 到 `main` 即可：

| 改动范围 | 触发的流水线 |
| --- | --- |
| `rollin-backend/**` | Deploy Backend |
| `rollin-frontend/**` | Deploy Frontend |
| `deploy/scripts/*.sh`、workflow 文件本身 | 对应那条 |

两条流水线完全独立——**只改前端不会重建后端镜像，反之亦然**。也可以随时在
Actions 页面手动 `Run workflow`。

### 5.2 后端流水线内部顺序

```
go vet + go test ─► 构建镜像推 ghcr ─► SSH：
    docker login（如配了令牌）
    mysqldump 备份到 /opt/rollin/backups/
    docker pull 新镜像
    migrate up --backup-path /backup/<dump>     ← 用新镜像跑，先迁移
    stop + rm 旧容器
    docker run 新容器（127.0.0.1:8080）
    轮询容器 HEALTHCHECK，最长 90s
        通过 → 记录当前版本号，成功退出
        失败 → 回滚到上一个镜像 ID 并重启；数据库迁移不会自动回退
```

### 5.3 前端流水线内部顺序

```
npm ci + lint + build ─► 打成 rollin-frontend-<sha>.tar.gz ─► SSH：
    解包到临时目录并校验 index.html
    移动到 /opt/rollin/releases/<sha>/
    ln + mv -T 原子切换 /var/www/rollin/current 符号链接
    nginx -t；失败则把符号链接切回上一版本
    systemctl reload nginx
    清理超出 KEEP 个的历史版本
```

### 5.4 回滚

**前端**（秒级、无状态）：

```bash
ssh deploy@<server>
ls -1t /opt/rollin/releases/                     # 找到想退回的版本
ln -sfn /opt/rollin/releases/<旧版本> /var/www/rollin/current.new
mv -Tf /var/www/rollin/current.new /var/www/rollin/current
sudo nginx -t && sudo systemctl reload nginx
```

或者直接把旧版本对应的提交 revert 后 push，让流水线重新部署。

**后端**：重新部署旧版本镜像。

```bash
ssh deploy@<server>
docker inspect -f '{{.Image}}' rollin-backend    # 当前镜像
cat /opt/rollin/backend-current-tag              # 上一次成功部署的 tag
# 找到目标 tag 后手动起容器（或把工作流指向该 tag 重跑）
docker pull ghcr.io/<owner>/rollin-backend:sha-<旧提交号>
docker stop rollin-backend && docker rm rollin-backend
docker run -d --name rollin-backend --restart unless-stopped \
  --env-file /etc/rollin/backend.env -p 127.0.0.1:8080:8080 \
  ghcr.io/<owner>/rollin-backend:sha-<旧提交号>
```

> **数据库迁移不支持自动降级。** 代码回滚不会回退 schema，只能靠 §7 的备份恢复。
> 涉及破坏性迁移时，务必先确认备份文件可用再做发布。

### 5.5 部署通知（飞书）

两条流水线都会在部署开始与部署结束时各推一张飞书互动卡片到研发群，前后端通知互相独立：

| 卡片 | 颜色 | 触发条件 |
| --- | --- | --- |
| 🚀 开始部署 | 蓝色 | deploy job 启动（构建通过、环境审批通过之后） |
| ✅ 部署成功 | 绿色 | `build` 与 `deploy` 两个 job 都成功（含冒烟检查） |
| ❌ 部署失败 | 红色 | 构建失败、部署中断、回滚、冒烟检查不过，卡片会标注失败阶段 |

卡片内容：提交号（7 位）、分支、触发者（手动触发会标注）、提交说明、用时，以及一个跳转
Actions 运行详情的按钮。**提交号与提交说明的取法**：push 触发时读本次推送的提交列表
（一次推多个提交会把清单一起列出，避免只看得到最后一个）；手动 `Run workflow` 时读工作区
git 的 HEAD。

配置步骤（Settings → Secrets and variables → Actions）：

1. 在目标飞书群里「群设置 → 群机器人 → 添加机器人 → 自定义机器人」，复制 webhook 地址；
2. 存为仓库 Secret `FEISHU_WEBHOOK`；
3. 机器人若开启了「签名校验」，把密钥存为 `FEISHU_WEBHOOK_SECRET`；未开启就留空，此时
   action 不会附签名参数。

行为约定：

- **`FEISHU_WEBHOOK` 没配时，通知步骤直接跳过并输出一条 warning**，流水线不受影响。
  想彻底关闭通知，把该 Secret 删掉即可。
- **通知失败不会把流水线判为失败**（地址失效、签名错误、网络不通都降级为 warning）。
  发送是否成功，看 Actions 日志里的 `飞书通知已发送` 或 `::warning::`。
- webhook 地址本身就是凭证（拿到就能往群里发消息），**只能放 Secrets，绝不写进 workflow 文件**；
  万一泄露，到飞书机器人设置里重置地址后更新 Secret。
- 卡片里的提交说明直接来自 commit message，提交信息里不要带凭证。

## 6. 验收清单

```bash
# 后端存活（仅在服务器本机；/healthz 不经边缘 nginx 暴露）
curl -s http://127.0.0.1:8080/healthz                 # {"status":"ok"}
docker ps --filter name=rollin-backend --format '{{.Status}}'   # Up ... (healthy)

# 端口暴露面检查：必须显示 127.0.0.1:8080，绝不能是 0.0.0.0:8080
ss -tlnp | grep -E '8080'

# 管理域名
curl -s https://admin.example.edu.cn/api/public/platform        # {"siteName":...}
curl -s -o /dev/null -w '%{http_code}\n' https://admin.example.edu.cn/        # 200
curl -s -o /dev/null -w '%{http_code}\n' https://admin.example.edu.cn/healthz # 404

# 候选人域名：Offer 深链可达（SPA fallback）
curl -s -o /dev/null -w '%{http_code}\n' https://t.example.edu.cn/o/<real-token>   # 200

# 白名单隔离验证（关键）
curl -s -o /dev/null -w '%{http_code}\n' https://t.example.edu.cn/api/platform/settings       # 404
curl -s -o /dev/null -w '%{http_code}\n' https://t.example.edu.cn/api/import/candidates       # 404
curl -s -o /dev/null -w '%{http_code}\n' https://t.example.edu.cn/api/activities/x/dashboard  # 404

# Import API（管理域名 + Bearer；契约见 docs/design/04-api-contract.md §8）
curl -X POST https://admin.example.edu.cn/api/import/candidates \
  -H "Authorization: Bearer rt_xxxx" -H "Content-Type: application/json" \
  -d '{"studentId":"2026010388","name":"张三","email":"zhangsan@example.edu.cn","score":92}'

# Token 不落 nginx 日志（A22）
sudo grep -c 'TOKEN_REDACTED' /var/log/nginx/rollin-offer.access.log   # 应有命中且无原始 token
```

域名隔离、并发语义、真实 SMTP 等只能在此环境完成的验收项，见
`docs/acceptance-record.md` §7。

## 7. 备份与恢复

备份由后端流水线在**每次迁移前**自动执行，落在服务器 `/opt/rollin/backups/`，
默认保留最近 7 份（`BACKUP_KEEP`）。日常建议再加一条宿主机 cron 做异地备份：

```bash
# /etc/cron.d/rollin-backup —— 每日 03:00 全量备份
0 3 * * * deploy cd /opt/rollin && docker run --rm --env-file /etc/rollin/backend.env \
  -v /opt/rollin/backups:/backup mysql:8.4 \
  sh -c 'export MYSQL_PWD="$MYSQL_PASSWORD"; mysqldump --single-transaction --routines --triggers --no-tablespaces -h "$MYSQL_HOST" -P "${MYSQL_PORT:-3306}" -u "$MYSQL_USER" "$MYSQL_DATABASE"' \
  | gzip > /opt/rollin/backups/rollin-$(date +\%F).sql.gz
```

恢复：

```bash
ssh deploy@<server>
docker stop rollin-backend
gunzip < /opt/rollin/backups/rollin-2026-09-19.sql.gz | \
  docker run --rm -i --env-file /etc/rollin/backend.env mysql:8.4 \
  sh -c 'export MYSQL_PWD="$MYSQL_PASSWORD"; exec mysql -h "$MYSQL_HOST" -P "${MYSQL_PORT:-3306}" -u "$MYSQL_USER" "$MYSQL_DATABASE"'
docker run --rm --env-file /etc/rollin/backend.env rollin-backend:latest migrate status
docker start rollin-backend
```

- Redis 只存 Session / 分布式锁 / 限流计数，丢失只影响「需要重新登录」，无需备份。
- `migrate up` 检测到旧库结构时**强制**要求 `--backup-path`；脚本已自动传。
  想显式跳过备份（仅限演练）可设 `SKIP_BACKUP=1`。

## 8. 日志与监控

```bash
docker logs -f rollin-backend                    # 应用 JSON 日志到 stdout
sudo tail -f /var/log/nginx/rollin-offer.access.log   # 候选人 Offer 页（URI 已脱敏）
sudo tail -f /var/log/nginx/access.log
```

- 应用日志已对 query 与公开 Token 路径段脱敏；nginx 侧 `/o/` 使用不含 URI 的日志格式。
- 镜像内置 `HEALTHCHECK`（GET `/healthz`，30s 间隔），`docker ps` 直接可见状态；
  nginx 也可对 `127.0.0.1:8080/healthz` 做存活探测。
- 业务异常巡检：后台「邮件任务」页（FAILED 队列）、审计日志；
  `mail_task.last_error` 含 SMTP 侧错误摘要。

## 9. 排障速查

| 现象 | 可能原因 | 处理 |
| --- | --- | --- |
| 流水线 `SSH 连接正常` 步骤失败 | host/port/user/key 有误，或 `known_hosts` 不匹配 | 核对 §3.1；本地用同一私钥手测 `ssh` |
| `docker: permission denied` | 部署用户不在 docker 组 | `sudo usermod -aG docker deploy`，重新登录 |
| `环境变量文件缺少必需项` | `/etc/rollin/backend.env` 未配置完整 | 按 §2.3 补齐 `MYSQL_*`、`SMTP_ENC_KEY`、`SUPER_ADMIN_*` |
| `mysqldump 失败` | 外部 MySQL 不可达 / 账号无权限 | 核对 `MYSQL_*`；确认账号有 `LOCK TABLES`/`SELECT` 权限；应急可临时 `SKIP_BACKUP=1`（**风险自担**） |
| `migrate up` 要求备份 | 检测到旧库结构 | 脚本已自动带备份；手动执行时补 `--backup-path` |
| 后端容器反复重启，日志报 schema 版本过低 | 迁移未执行 | 手动 `docker run --rm --env-file /etc/rollin/backend.env <image> migrate up` |
| 新版本健康检查不过并回滚 | 新代码/新配置有问题 | 看回滚前的容器日志（脚本已打印最近 40 行）；数据库迁移可能已生效，需人工核对 |
| 页面 502 Bad Gateway | 后端容器挂了，或端口不是 127.0.0.1:8080 | `docker ps`；核对 nginx `upstream rollin_backend` |
| 页面白屏 / 资源 404 | 前端产物目录为空或符号链接指错 | `ls -l /var/www/rollin/current`；`ls /opt/rollin/releases/` |
| `nginx -t` 失败导致前端回滚 | 配置语法错误 | 在服务器上手动 `sudo nginx -t` 看具体行 |
| `systemctl reload nginx 失败` | 部署用户无 sudo 权限 | 按 §2.5 配置 sudoers 并设仓库变量 `DEPLOY_SUDO=sudo` |
| 登录/写操作 403 FORBIDDEN | CSRF Origin 校验失败：nginx 改写了 Host，或来源域名未加白 | 确认 `proxy_set_header Host $host;`；跨域名来源加入 `CSRF_ALLOWED_ORIGINS` |
| 登录后 Cookie 不生效（一直 401） | http 环境下 `COOKIE_SECURE=true` | 生产必须 https；本地联调用 `docker-compose.dev.yml`（已内置 `COOKIE_SECURE=false`） |
| 登录返回 429 RATE_LIMITED | 触发登录限流（IP+邮箱 5 次/15 分钟） | 等窗口过去；Redis 故障时限流 fail-open，可查 Redis |
| Import 返回 404 TOKEN_INVALID | Token 写错或已吊销 | 后台重新生成；原文仅创建时展示一次 |
| 邮件不发 / `SMTP_NOT_CONFIGURED` | 平台或活动作用域 SMTP 未配置/未验证 | 后台「SMTP 配置」保存并测试；两个作用域相互独立、不回退 |
| 邮件任务 FAILED | SMTP 连接/认证失败（详情见 `lastError`） | 后台「邮件任务」页重试；163/QQ 用 465 + `SSL` + 授权码 |
| 容器内连不上 MySQL/Redis | `.env` 里 `MYSQL_HOST`/`REDIS_HOST` 误填 127.0.0.1 | 容器内 127.0.0.1 指向容器自身，必须填外部服务地址 |

## 10. 安全注意事项

- 后端端口仅回环可达（§1）。任何端口转发/容器网络调整都不得把 8080 暴露到公网。
- `/etc/rollin/backend.env` 含 `SMTP_ENC_KEY`、超管初始密码与数据库凭证：仅存于服务器，
  权限 600，不入 Git、不进流水线日志。
- 部署私钥只用于本仓库部署，建议在服务器上限制来源 IP；泄露后立即更换
  （重新生成密钥对 + 更新 `SSH_KEY` + 从 `authorized_keys` 移除旧公钥）。
- `GHCR_TOKEN` 建议最小权限（只勾 `read:packages`），并设置合理有效期。
- 脚本在拉取镜像后会 `docker logout ghcr.io`，避免令牌长期留在服务器
  `~/.docker/config.json`（如需保留可设 `GHCR_LOGOUT=0`）。
- `SMTP_ENC_KEY` 轮换会使已落库的 SMTP 密码密文无法解密，需在平台/活动 SMTP 配置里重新录入。
- Cookie 安全属性由 `COOKIE_SECURE` / `SESSION_HOURS` 控制；https 环境必须
  `COOKIE_SECURE=true`。
- 服务器防火墙只放行 80/443 与你的 SSH 端口。
