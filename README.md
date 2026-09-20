# Rollin —— 滚动录取平台 V1

Rollin 是面向高校/部门招新的滚动录取平台：外部报名系统通过 Import API 单人导入候选人，
活动负责人启动正式录取后，系统按排名自动发放 Offer 邮件，候选人在专属页面一键接受/放弃，
递补、过期结算、邮件重试全部自动完成，并保留完整审计日志。

- 后端：Go 1.25 + chi + GORM + MySQL 8.4 + Redis 7（单二进制，HTTP 单端口 `:8080`，同时承载 `migrate` 子命令）
- 前端：React 19 + Vite SPA（构建产物由容器内 nginx 静态交付，API 同源经边缘 nginx 反代）
- 设计文档：`docs/design/`（API 契约 `04-api-contract.md`、状态机、数据模型、迁移方案等）

## 双域名架构

| 域名 | 面向 | SPA 路由 | API 白名单 | 说明 |
| --- | --- | --- | --- | --- |
| 候选人域名 `t.xxx.xxx` | 候选人 | `/o/:token` | 仅 `/api/public/*` | Offer 查看/接受/放弃/终态展示；不提供任何管理入口 |
| 管理域名 `admin.xxx.xxx` | 管理员 + 外部系统 | `/login`、`/login/platform`、`/invite/:token`、`/platform/*`、`/a/:activitySlug/*` | 全部 `/api/*`（含 `/api/import/*` Bearer 调用、`/api/platform`、`/api/activities`、`/api/public`） | 后台管理、成员邀请激活、外部导入 |

- 后端（chi）只监听单端口；**域名隔离完全由边缘 nginx 白名单实现**（`deploy/nginx.conf.example`）。
  候选人域名上 `/api/*` 中除 `/api/public/*` 外一律 404。
- 因此**后端端口绝不能对公网开放**（compose 已只映射 `127.0.0.1`，防火墙不得放行），
  直连后端端口即绕过域名限制。
- 链接拼接：Offer 邮件链接 = `CANDIDATE_BASE_URL + /o/{token}`；邀请邮件链接 = `ADMIN_BASE_URL + /invite/{token}`。
- 两个域名也可合并部署为同域（把 `ADMIN_BASE_URL` 设成与 `CANDIDATE_BASE_URL` 相同即可，前端路由互不冲突）。

## 目录结构

```
rollin/
├── README.md                  本文件
├── .github/                   部署流水线（GitHub Actions）
│   ├── actions/prepare-ssh/   两条流水线共用的 SSH 准备步骤
│   └── workflows/
│       ├── deploy-backend.yml 后端：vet/test → 构建推 ghcr.io → SSH 备份/迁移/换容器/回滚
│       └── deploy-frontend.yml 前端：npm ci/lint/build → tar → scp → 原子切换 → nginx reload
├── docker-compose.dev.yml     仅本地开发/验收（自带 MySQL 8.4 + Redis 7，端口仅绑 127.0.0.1）
├── docker-compose.legacy.yml  已废弃的旧双容器部署方式，留档用，勿在生产使用
├── deploy/
│   ├── README.md              部署手册：服务器准备、Secrets 清单、发版、回滚、排障
│   ├── nginx.conf.example     宿主机 nginx：TLS + 双域名白名单隔离 + 前端静态交付
│   └── scripts/
│       ├── backend-deploy.sh  服务器端后端部署（备份 → pull → migrate → 换容器 → 健康门禁/回滚）
│       └── frontend-deploy.sh 服务器端前端部署（解包 → 原子切换 → nginx reload → 清理旧版本）
├── docs/design/               设计文档（API 契约、状态机、权限矩阵、数据模型、迁移）
├── 需求说明.md / 执行计划.md
├── rollin-backend/            Go 后端
│   ├── Dockerfile             多阶段构建：非 root、UTC、/healthz 健康检查
│   ├── .env.example           环境变量清单（复制为 .env 使用；示例不含真实凭证）
│   ├── cmd/server/            入口：HTTP 服务 + migrate up|status|precheck 子命令
│   └── internal/             领域包（auth/activity/application/offer/mail/…）
│       └── db/migrations/    Go 迁移（单版 V1；通过 migrate up 应用，服务启动有只读版本门禁）
└── rollin-frontend/           React 19 + Vite SPA
    ├── src/                   业务源码（构建产物 dist/ 由宿主机 nginx 交付）
    ├── nginx.conf             仅容器化时的参考配置；生产部署不使用（前端不跑 Docker）
    └── Dockerfile             同上，仅备用；生产部署不使用
```

## 生产部署（GitHub Actions 自动化）

前端后端**分开部署**，push 到 `main` 即自动发布：

| 组件 | 方式 |
| --- | --- |
| 前端 | Actions 构建 Vite 产物 → 传到服务器磁盘 → 宿主机 nginx 直接从目录交付（**不用 Docker**），靠 `current` 符号链接原子切换、秒级回滚 |
| 后端 | Actions 构建镜像推 `ghcr.io` → SSH 在服务器 `docker pull` + `docker run`（**不用 docker compose**），迁移前自动备份，健康检查不过自动回滚 |
| MySQL / Redis | **外部服务**，由运维提供，流水线与本仓脚本都不启动数据库容器 |

完整的一站式手册（服务器一次性准备、Secrets 清单、发版与回滚、验收、排障）见
**[deploy/README.md](deploy/README.md)**。概要：

```bash
# 1) 服务器准备：docker + nginx(>=1.25.1) + 目录 + 环境变量文件（只需一次）
sudo mkdir -p /etc/rollin /opt/rollin/releases /var/www/rollin
sudo cp rollin-backend/.env.example /etc/rollin/backend.env && sudo chmod 600 /etc/rollin/backend.env
#    填写外部 MySQL/Redis 地址与凭证、CANDIDATE_BASE_URL / ADMIN_BASE_URL、SMTP_ENC_KEY

# 2) 部署 nginx
sudo cp deploy/nginx.conf.example /etc/nginx/conf.d/rollin.conf   # 替换域名与证书路径
sudo nginx -t && sudo systemctl reload nginx

# 3) 配置仓库 Secrets：SSH_HOST / SSH_USER / SSH_KEY（可选 SSH_PORT、SSH_KNOWN_HOSTS、GHCR_TOKEN）
#    可选 Variables：ADMIN_BASE_URL / CANDIDATE_BASE_URL（用于部署后冒烟检查）

# 4) 推代码即部署
git push origin main
```

流水线内部顺序：后端 `go vet`+`go test` → 构建推镜像 → 备份 → `migrate up` →
换容器 → 健康门禁（失败回滚）；前端 `npm ci`+`lint`+`build` → 打包 → 原子切换 → `nginx -t`+reload。

- **后端端口只绑 `127.0.0.1:8080`**，绝不能对公网开放（域名隔离靠 nginx 白名单实现）。
- `/healthz` 在 nginx 上显式 404，只允许服务器本机访问。
- 迁移不支持自动降级，代码回滚不会回退 schema，恢复依赖备份。


## 本地开发快速开始

前置依赖：Go 1.25+、Node 22+、Docker（含 compose 插件）。Windows/macOS/Linux 均可。

```bash
# 1) 准备环境变量（.env.example 可逐字使用，本地凭证均为演示值）
cp rollin-backend/.env.example rollin-backend/.env

# 2) 起依赖容器（MySQL 8.4 + Redis 7；端口只映射到 127.0.0.1）
docker compose -f docker-compose.dev.yml up -d mysql redis

# 3) 执行数据库迁移（必须先于服务启动：启动门禁要求 schema 已达最低版本）
#    方式 A：用容器执行（注意只传子命令：镜像 ENTRYPOINT 已是 /app/rollin-server，
#            再传一次二进制路径会把子命令挤到第二位，进程会当成「启动服务」）
docker compose -f docker-compose.dev.yml run --rm backend migrate up
#    方式 B：用本机 Go 执行（自动读取 rollin-backend/.env）
cd rollin-backend && go run ./cmd/server migrate up && cd ..

# 4) 启动后端（二选一）
docker compose -f docker-compose.dev.yml up -d backend     # 容器方式
cd rollin-backend && go run ./cmd/server                   # 本机方式（.env 里 127.0.0.1 即宿主端口映射）

# 5) 验证
curl http://127.0.0.1:8080/healthz        # → {"status":"ok"}
```

说明：

- 当前迁移已合并为单版 `V1__initial_schema`，包含完整业务表、`mail_task.payload` 和平台参数默认值；最低数据库版本为 V1。空库仍须先执行第 3 步，再启动服务。
- 一步构建全部：`docker compose -f docker-compose.dev.yml up --build`（空库时 backend 会先因
  「schema 未就绪」报错重启，执行第 3 步的 `migrate up` 后自动恢复，属预期）。
- dev compose 内 MySQL 凭证默认 `rollin / rollin_dev`，与逐字复制的 `.env.example` 自动匹配；
  覆盖方式见该文件头部注释。本地均为弱口令，严禁用于可被外网访问的环境。
- 超管引导：首次启动用 `SUPER_ADMIN_EMAIL` / `SUPER_ADMIN_INITIAL_PASSWORD` 幂等创建
  平台超级管理员（仅 `platform_admin` 空表时生效，永不改密），随后用其在
  `http://127.0.0.1:8080` 对应的 SPA 登录页（管理域名 `/login/platform`）登录。
- 前端开发循环：`cd rollin-frontend && npm install && npm run dev`（Vite，默认
  `http://localhost:5173`）。`VITE_API_BASE` 未设置时 API 走同源 `/api`，Vite 开发服务器会
  自动代理到 `http://127.0.0.1:8080`；如后端地址不同，设置 `VITE_DEV_API_TARGET` 覆盖代理目标。
- 生产形态不使用 dev compose：前端由宿主机 nginx 交付静态产物，后端用 `docker run`
  单容器，MySQL/Redis 走外部服务（见下文「生产部署」与 `deploy/README.md`）。

## 环境变量

后端全部变量定义于 `rollin-backend/internal/config/config.go`，示例清单见
`rollin-backend/.env.example`。缺必需项时进程拒绝启动。

| 变量 | 用途 | 示例 | 默认 |
| --- | --- | --- | --- |
| `HTTP_ADDRESS` | 后端 HTTP 监听地址（容器内勿改，健康检查按 8080） | `:8080` | `:8080` |
| `MYSQL_HOST` | 外部 MySQL 地址（容器内部署时不可填 127.0.0.1） | `mysql.internal.example.edu.cn` | `127.0.0.1` |
| `MYSQL_PORT` | MySQL 端口 | `3306` | `3306` |
| `MYSQL_DATABASE` | 库名 | `rollin` | `rollin` |
| `MYSQL_USER` | 账号 | `rollin` | `rollin` |
| `MYSQL_PASSWORD` | 密码（必填，无默认） | （自行生成，勿提交） | 空 |
| `MYSQL_AUTO_CREATE_DATABASE` | 启动时库不存在则自动建库 | `true` | `true` |
| `REDIS_HOST` | 外部 Redis 地址（Session/分布式锁/限流） | `redis.internal.example.edu.cn` | `127.0.0.1` |
| `REDIS_PORT` | Redis 端口 | `6379` | `6379` |
| `REDIS_PASSWORD` | Redis 密码 | （自行生成） | 空 |
| `REDIS_DB` | Redis 逻辑库 | `0` | `0` |
| `CANDIDATE_BASE_URL` | 候选人域名，Offer 邮件链接用它拼接 | `https://t.example.edu.cn` | `https://t.example.edu.cn`（别名 `PUBLIC_BASE_URL`） |
| `ADMIN_BASE_URL` | 管理域名，邀请邮件链接用它拼接 | `https://admin.example.edu.cn` | 回退 `CANDIDATE_BASE_URL` |
| `SMTP_ENC_KEY` | SMTP 密码落库加密密钥，base64 的 32 字节（AES-256-GCM）。生成：`openssl rand -base64 32`。**轮换会使已存 SMTP 密码无法解密，需重新录入** | （base64 串） | 必填 |
| `SUPER_ADMIN_NAME` | 引导超管显示名 | `超级管理员` | `超级管理员` |
| `SUPER_ADMIN_EMAIL` | 引导超管邮箱（必填；仅空表时创建） | `root@example.edu.cn` | 必填 |
| `SUPER_ADMIN_INITIAL_PASSWORD` | 引导超管初始密码（必填；别名 `SUPER_ADMIN_PASSWORD`；永不覆盖已有密码） | （强密码，首登后修改） | 必填 |
| `COOKIE_SECURE` | Session Cookie 加 Secure；生产 https 必须为 `true`，本地 http 联调为 `false` | `true` | `true` |
| `SESSION_HOURS` | 会话时长（小时），同时喂给平台参数 sessionHours | `24` | `24` |
| `CSRF_ALLOWED_ORIGINS` | CSRF Origin 白名单的额外 host（逗号分隔，除请求 Host 外） | `admin.xxx.xxx,localhost:5173` | 空 = 仅请求 Host |
| `LOGIN_RATE_LIMIT` | 登录限流阈值（IP+邮箱 固定窗口） | `5` | `5` |
| `LOGIN_RATE_WINDOW_MINUTES` | 登录限流窗口（分钟） | `15` | `15` |
| `MAIL_SMTP_TIMEOUT_SECONDS` | 邮件 Worker 单次 SMTP 发送超时（config 支持但 .env.example 未列，可不配） | `30` | `30` |
| `VITE_API_BASE`（前端构建参数） | 前端 API 基地址；构建期注入产物。留空 = 同源（推荐，经边缘 nginx 反代） | 空 或 `https://admin.example.edu.cn` | 空 |
| `VITE_DEV_API_TARGET`（Vite 开发环境） | `/api` 代理目标；仅开发服务器使用 | `http://127.0.0.1:8080` | `http://127.0.0.1:8080` |

## Import API（外部报名系统 → Rollin）

- 端点：`POST /api/import/candidates`，走**管理域名**（候选人域名不放行 `/api/import/*`）。
- 认证：`Authorization: Bearer <Import Token>`，Token 在后台「活动工作区 → Import Token」创建，
  原文仅创建时展示一次；`/api/import/*` 免 Cookie/CSRF。
- 每次只导入**一个**候选人（单对象，传数组返回 `VALIDATION_ERROR`）；仅接受
  `studentId/name/email/score` 四个字段；按 `studentId` 幂等（未冻结排名时重复导入为更新，
  返回 `created=false`）。

```bash
curl -X POST "https://admin.example.edu.cn/api/import/candidates" \
  -H "Authorization: Bearer rt_9f2Kxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{"studentId":"2026010388","name":"张三","email":"zhangsan@example.edu.cn","score":92}'
```

成功：`201`（新建）/ `200`（幂等命中）：

```json
{ "created": true, "applicationId": 101, "candidateId": 55, "activityId": 2, "status": "WAITING", "rankingDirty": true }
```

常见错误：`TOKEN_INVALID`(404，Token 不存在或已吊销)、`TOKEN_EXPIRED`(401)、
`RANKING_FROZEN`(403，排名已冻结禁止导入)、`ACTIVITY_DISABLED/ACTIVITY_ARCHIVED`(403)、
`RATE_LIMITED`(429)。完整契约见 `docs/design/04-api-contract.md` §8。

## 备份与恢复

线上备份由**后端流水线在每次迁移前自动执行**（落在服务器 `/opt/rollin/backups/`，
默认保留最近 7 份）；日常异地备份与恢复演练由外部运维负责。应用层仅提供迁移前校验与提示。

```bash
# 备份（服务器上；也可配成 cron 每日执行，见 deploy/README.md §7）
docker run --rm --env-file /etc/rollin/backend.env -v /opt/rollin/backups:/backup mysql:8.4 \
  sh -c 'export MYSQL_PWD="$MYSQL_PASSWORD"; mysqldump --single-transaction --routines --triggers \
         --no-tablespaces -h "$MYSQL_HOST" -P "${MYSQL_PORT:-3306}" -u "$MYSQL_USER" "$MYSQL_DATABASE"' \
  > /opt/rollin/backups/rollin-$(date +%F).sql

# 恢复（停应用 → 导入 → 核对 schema 版本 → 起应用）
docker stop rollin-backend
docker run --rm -i --env-file /etc/rollin/backend.env mysql:8.4 \
  sh -c 'export MYSQL_PWD="$MYSQL_PASSWORD"; exec mysql -h "$MYSQL_HOST" -P "${MYSQL_PORT:-3306}" \
         -u "$MYSQL_USER" "$MYSQL_DATABASE"' < /opt/rollin/backups/rollin-2026-09-19.sql
docker run --rm --env-file /etc/rollin/backend.env rollin-backend:latest migrate status
docker start rollin-backend
```

- `migrate up` 检测到旧库结构时会强制要求 `--backup-path <file>`（或显式
  `--allow-no-backup` 自担风险）；空库首次建表不强制。
- 迁移 checksum 与代码绑定：已应用的迁移被修改会导致服务拒绝启动（防篡改门禁）。
- Redis 仅存 Session/锁/限流计数，丢失只需重新登录，不参与恢复。

## 故障排查速查

线上（GitHub Actions 部署）的完整排障表见 `deploy/README.md` §9；本地开发排查：

| 现象 | 可能原因 | 处理 |
| --- | --- | --- |
| backend 反复重启，日志报 schema 版本低于最低版本 | 未执行迁移 | `docker run --rm --env-file rollin-backend/.env rollin-backend:latest migrate up` |
| `curl http://127.0.0.1:8080/healthz` 不通 | 容器未起/端口未映射/改过 `HTTP_ADDRESS` | `docker ps`、`docker logs rollin-backend`；端口改动需同步镜像健康检查 |
| 页面 502 Bad Gateway | 后端容器挂了，或端口与 nginx `upstream` 不一致 | `docker ps`；核对 `deploy/nginx.conf.example` 中 `127.0.0.1:8080` |
| 页面白屏 / 静态资源 404 | 前端产物目录为空或 `current` 符号链接指错 | `ls -l /var/www/rollin/current`；`ls /opt/rollin/releases/` |
| 登录/写操作 403 FORBIDDEN | CSRF Origin 校验失败：nginx 改写了 Host，或来源域名未加白 | 确认 `proxy_set_header Host $host;`；跨域名来源加入 `CSRF_ALLOWED_ORIGINS` |
| 登录后 Cookie 不生效（一直 401） | http 环境下 `COOKIE_SECURE=true`，浏览器不回传 Cookie | 生产用 https；本地 dev compose 已强制 `false` |
| 登录返回 429 RATE_LIMITED | 触发登录限流（IP+邮箱 5 次/15 分钟） | 等待窗口过去；Redis 故障时限流 fail-open，可查 Redis |
| Import 返回 404 TOKEN_INVALID | Token 写错或已吊销 | 后台重新生成；注意原文仅创建时展示一次 |
| Import 返回 401 TOKEN_EXPIRED | Token 已过期 | 后台新建 Import Token |
| 邮件不发 / `SMTP_NOT_CONFIGURED` | 平台或活动作用域 SMTP 未配置/未验证 | 后台「SMTP 配置」保存并测试；活动与平台作用域相互独立、不回退 |
| 邮件任务 FAILED | SMTP 连接/认证失败（详情见 `lastError`） | 后台「邮件任务」页重试；163/QQ 用 465 + `SSL` + 授权码 |
| `migrate up` 要求备份 | 检测到旧库结构 | `--backup-path` 指定 mysqldump 文件，或 `--allow-no-backup` 自担风险 |
| 容器内连不上 MySQL/Redis | `.env` 里 `MYSQL_HOST`/`REDIS_HOST` 误填 127.0.0.1 | 容器内 127.0.0.1 指向容器自身，填外部服务地址（dev compose 由 `environment` 覆盖为服务名） |
| 候选人域名打开管理路径 | nginx 已做白名单隔离（页面 404 / API 404） | 属预期；如需放开根路径提示页见 nginx 示例注释 |
