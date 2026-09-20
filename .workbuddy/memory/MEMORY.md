# Rollin 项目长期约定

## 部署（2026-09-20 定型）

**架构**：GitHub Actions 自动部署，前后端完全分开；MySQL / Redis 一律外部提供，
**全流程不使用 Docker Compose**；前端不使用 Docker（宿主机 nginx 直接交付静态产物），
后端使用 Docker（纯 `docker run` 单容器）。

| 项 | 约定 |
| --- | --- |
| 镜像仓库 | `ghcr.io/<owner>/rollin-backend`，tag 形如 `sha-<7位提交号>`（另打 `latest`） |
| 部署通道 | SSH 直连（原生 ssh/scp），已构建 `~/.ssh/config` 的 `rollin-deploy` 别名 |
| 域名 | 双域名：候选人 `t.xxx`（仅 `/o/:token` + `/api/public/*`）、管理 `admin.xxx`（全部 `/api/*`） |
| 后端端口 | 只绑 `127.0.0.1:8080`，绝不对公网开放（域名隔离全靠 nginx 白名单） |
| 前端目录 | 产物在 `/opt/rollin/releases/<sha>/`，`/var/www/rollin/current` 为指向它的符号链接 |
| 后端环境变量 | 服务器 `/etc/rollin/backend.env`（600，唯一存真实凭证处，不入 git） |
| 迁移 | 由后端流水线在换容器**之前**执行；迁移前自动 mysqldump 到 `/opt/rollin/backups/`（默认留 7 份） |
| 数据库降级 | 不支持自动降级；代码回滚不回退 schema，恢复只能靠备份 |

**关键文件**

- `.github/workflows/deploy-backend.yml` / `deploy-frontend.yml`
- `.github/actions/prepare-ssh/action.yml`
- `deploy/scripts/backend-deploy.sh` / `frontend-deploy.sh`
- `deploy/nginx.conf.example`（宿主机 nginx，双域名 + 白名单）
- `deploy/README.md`（部署手册，改部署逻辑时同步更新）

**易踩的坑**

1. **迁移命令只能传子命令**：镜像 ENTRYPOINT 已是 `/app/rollin-server`，
   写 `docker run IMAGE /app/rollin-server migrate up` 会把二进制当 `os.Args[1]`，
   `main()` 判 `args[0] == "migrate"` 失败 → 进程被当成「启动服务」。
   正确：`docker run --rm --env-file ... IMAGE migrate up`。
2. **`nginx -t` 需要 root**：certbot 证书 `privkey.pem` 仅 root 可读，非 root 跑必然
   Permission denied，会误判发布失败。前端脚本已把 `nginx -t` 提到改动之前，并提示
   `DEPLOY_SUDO=sudo`；sudoers 需放行 `/usr/sbin/nginx -t` 与 `/usr/bin/systemctl reload nginx`。
3. **`/healthz` 要在 nginx 上显式 404**，否则会被 SPA fallback 命中而 200，暴露存活信息。
4. **候选人域名 `/o/` 不能把 URI 写进访问日志**：已用不含 `$request` 的 `log_format`
   （`rollin_offer_safe`）防止一次性 Offer Token 落盘（验收项 A22）。
5. `docker-compose.yml` 已改名为 `docker-compose.legacy.yml` 留档，**勿用于生产**。
   `docker-compose.dev.yml` 仅供本地开发/验收。

**仓库状态**：截至 2026-09-20 **尚未 git init**，需要用户自行初始化并推到 GitHub，
流水线才能生效。
