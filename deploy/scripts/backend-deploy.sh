#!/usr/bin/env bash
# =============================================================================
# Rollin 后端部署脚本（在目标服务器上执行）
# =============================================================================
# 由 .github/workflows/deploy-backend.yml 通过 SSH 调用（`ssh host 'bash -s' < 本文件`），
# 也可以登录服务器后手动执行。整个流程幂等，可重复运行。
#
# 做的事，按顺序：
#   1. 前置检查（docker / 环境变量文件 / 镜像名）
#   2. 可选 docker login ghcr.io（私有包需要；公开包可跳过）
#   3. mysqldump 备份（用 mysql 官方镜像跑，服务器无需装 mysql 客户端）
#   4. docker pull 新镜像
#   5. 执行 migrate up（用新镜像跑，迁移先于换容器）
#   6. 原子替换容器（stop + rm + run）
#   7. 健康门禁：轮询容器 HEALTHCHECK，不通则回滚到上一个镜像
#
# 明确不使用 docker compose：MySQL / Redis 均为外部服务，应用容器用 docker run 管理。
#
# 输入（环境变量，全部可用默认值）：
#   IMAGE_REPO       必填  例如 ghcr.io/xingyuan/rollin-backend
#   IMAGE_TAG        必填  例如 sha-1a2b3c4
#   GHCR_USER        可选  ghcr.io 用户名（配 GHCR_TOKEN 时使用）
#   GHCR_TOKEN       可选  ghcr.io 读取令牌；留空则假定包为 public
#   GHCR_LOGOUT      默认 1    拉取后登出 ghcr.io（避免令牌留在服务器 config.json）
#   ENV_FILE         默认 /etc/rollin/backend.env   （600 权限，含真实凭证）
#   CONTAINER_NAME   默认 rollin-backend
#   HOST_BIND        默认 127.0.0.1；双服务器部署时设为后端私网地址或 0.0.0.0
#   HOST_PORT        默认 12820（容器内仍监听 8080；防火墙只允许前端服务器访问）
#   STATE_DIR        默认 /opt/rollin
#   BACKUP_DIR       默认 $STATE_DIR/backups
#   BACKUP_KEEP      默认 7    保留最近几个备份
#   HEALTH_TIMEOUT   默认 90   健康等待秒数
#   MYSQL_IMAGE      默认 mysql:8.4（仅用于 mysqldump）
#   SKIP_BACKUP      默认 0    置 1 跳过备份（仅限明知的空库/演练）
#   SKIP_MIGRATE     默认 0    置 1 跳过迁移（仅限纯回滚场景）
# =============================================================================
set -Eeuo pipefail

IMAGE_REPO="${IMAGE_REPO:?必须提供 IMAGE_REPO，例如 ghcr.io/<owner>/rollin-backend}"
IMAGE_TAG="${IMAGE_TAG:?必须提供 IMAGE_TAG，例如 sha-1a2b3c4}"
GHCR_USER="${GHCR_USER:-}"
GHCR_TOKEN="${GHCR_TOKEN:-}"
GHCR_LOGOUT="${GHCR_LOGOUT:-1}"
ENV_FILE="${ENV_FILE:-/etc/rollin/backend.env}"
CONTAINER_NAME="${CONTAINER_NAME:-rollin-backend}"
HOST_BIND="${HOST_BIND:-127.0.0.1}"
HOST_PORT="${HOST_PORT:-12820}"
STATE_DIR="${STATE_DIR:-/opt/rollin}"
BACKUP_DIR="${BACKUP_DIR:-${STATE_DIR}/backups}"
BACKUP_KEEP="${BACKUP_KEEP:-7}"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-90}"
MYSQL_IMAGE="${MYSQL_IMAGE:-mysql:8.4}"
SKIP_BACKUP="${SKIP_BACKUP:-0}"
SKIP_MIGRATE="${SKIP_MIGRATE:-0}"
CANDIDATE_BASE_URL="${CANDIDATE_BASE_URL:-}"
ADMIN_BASE_URL="${ADMIN_BASE_URL:-}"

IMAGE_REF="${IMAGE_REPO}:${IMAGE_TAG}"
CURRENT_TAG_FILE="${STATE_DIR}/backend-current-tag"
MIGRATE_MOUNT=()

log()  { printf '[backend] %s\n' "$*"; }
warn() { printf '[backend][WARN] %s\n' "$*" >&2; }
die()  { printf '[backend][ERROR] %s\n' "$*" >&2; exit 1; }

on_error() { die "脚本在第 ${1} 行失败，部署中断（容器未被替换或已按需回滚）"; }
trap 'on_error $LINENO' ERR

case "$BACKUP_KEEP" in ''|*[!0-9]*) die "BACKUP_KEEP 必须是非负整数，当前值：${BACKUP_KEEP}" ;; esac
case "$HEALTH_TIMEOUT" in ''|*[!0-9]*) die "HEALTH_TIMEOUT 必须是正整数秒数，当前值：${HEALTH_TIMEOUT}" ;; esac
[ "$HEALTH_TIMEOUT" -gt 0 ] || die "HEALTH_TIMEOUT 必须大于 0"

# -----------------------------------------------------------------------------
# 1. 前置检查
# -----------------------------------------------------------------------------
log "开始部署 ${IMAGE_REF}"

command -v docker >/dev/null 2>&1 || die "服务器上没有 docker，请先安装"
docker info >/dev/null 2>&1 || die "docker 守护进程不可用（当前用户可能不在 docker 组）"
[ -r "$ENV_FILE" ] || die "环境变量文件不可读：${ENV_FILE}（首次部署请按 deploy/README.md 创建）"

# --env-file 由 docker 解析；这里只做一次「必需键存在」的轻量校验，不回显任何值
for key in MYSQL_HOST MYSQL_USER MYSQL_DATABASE SMTP_ENC_KEY SUPER_ADMIN_EMAIL SUPER_ADMIN_INITIAL_PASSWORD; do
    grep -qE "^[[:space:]]*${key}=.+" "$ENV_FILE" || die "环境变量文件缺少必需项：${key}"
done
log "环境变量文件校验通过：${ENV_FILE}"

mkdir -p "$STATE_DIR" "$BACKUP_DIR"

# -----------------------------------------------------------------------------
# 2. ghcr.io 登录（私有包必需；公开包留空 GHCR_TOKEN 即可）
# -----------------------------------------------------------------------------
if [ -n "$GHCR_TOKEN" ]; then
    [ -n "$GHCR_USER" ] || die "提供了 GHCR_TOKEN 就必须同时提供 GHCR_USER"
    log "登录 ghcr.io（用户 ${GHCR_USER}）"
    printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USER" --password-stdin >/dev/null
else
    log "未提供 GHCR_TOKEN，按公开包处理，跳过 docker login"
fi

# -----------------------------------------------------------------------------
# 3. 数据库备份（迁移前的安全网）
# -----------------------------------------------------------------------------
backup_name=""
if [ "$SKIP_BACKUP" = "1" ]; then
    warn "SKIP_BACKUP=1，已跳过数据库备份（迁移将无法提供 --backup-path）"
else
    backup_name="rollin-$(date -u +%Y%m%dT%H%M%SZ).sql"
    backup_path="${BACKUP_DIR}/${backup_name}"
    log "备份数据库到 ${backup_path}"
    # 备份不需要挂卷：mysqldump 写 stdout，宿主机重定向落盘
    # 密码经容器内环境变量传递，不出现在宿主机进程列表；--no-tablespaces 兼容低权限账号
    docker run --rm --env-file "$ENV_FILE" "$MYSQL_IMAGE" \
        sh -c 'export MYSQL_PWD="$MYSQL_PASSWORD"; exec mysqldump \
                 --single-transaction --routines --triggers --no-tablespaces \
                 -h "$MYSQL_HOST" -P "${MYSQL_PORT:-3306}" -u "$MYSQL_USER" "$MYSQL_DATABASE"' \
        > "$backup_path" \
        || die "mysqldump 失败（检查 MYSQL_* 与账号权限，或确认已允许跳过）"
    [ -s "$backup_path" ] || die "备份文件为空：${backup_path}"
    log "备份完成：$(du -h "$backup_path" | cut -f1)"

    # 轮转旧备份（ls 无匹配时不应让 set -e 中断脚本）
    # shellcheck disable=SC2012
    ls -1t "${BACKUP_DIR}"/rollin-*.sql 2>/dev/null | tail -n +"$((BACKUP_KEEP + 1))" | while read -r old; do
        log "清理旧备份 $(basename "$old")"
        rm -f -- "$old"
    done || true
fi

# -----------------------------------------------------------------------------
# 4. 拉取新镜像
# -----------------------------------------------------------------------------
# 先记下当前在跑的镜像 ID，作为回滚目标（必须在 pull 之前，避免 pull 改写标签影响判定）
previous_image_id=""
if docker inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    previous_image_id="$(docker inspect -f '{{.Image}}' "$CONTAINER_NAME")"
    log "当前容器镜像 ID（回滚目标）：${previous_image_id}"
fi

log "拉取镜像 ${IMAGE_REF}"
docker pull "$IMAGE_REF" >/dev/null
log "镜像拉取完成"

# 镜像已在本地，运行期不再需要 registry 凭证；及时登出避免把令牌留在 ~/.docker/config.json
if [ -n "$GHCR_TOKEN" ] && [ "$GHCR_LOGOUT" = "1" ]; then
    docker logout ghcr.io >/dev/null 2>&1 || warn "docker logout ghcr.io 失败，请手工检查服务器上的 ~/.docker/config.json"
fi

# -----------------------------------------------------------------------------
# 5. 数据库迁移（先迁移、后换容器；启动门禁要求 schema 已就绪）
# -----------------------------------------------------------------------------
# 注意：镜像 ENTRYPOINT 已经是 /app/rollin-server，这里传的是「子命令」而不是二进制路径。
# 写成 `/app/rollin-server migrate up` 会把二进制当成第一个参数，main() 会当成启动服务。
if [ "$SKIP_MIGRATE" = "1" ]; then
    warn "SKIP_MIGRATE=1，已跳过数据库迁移"
else
    migrate_args=(migrate up)
    if [ -n "$backup_name" ]; then
        MIGRATE_MOUNT=(-v "${BACKUP_DIR}:/backup:ro")
        migrate_args+=(--backup-path "/backup/${backup_name}")
    else
        migrate_args+=(--allow-no-backup)
    fi
    log "执行迁移：${migrate_args[*]}"
    # ${arr[@]+...} 形式保证空数组在 set -u 下也能安全展开（兼容 bash 4.3）
    docker run --rm --env-file "$ENV_FILE" ${MIGRATE_MOUNT[@]+"${MIGRATE_MOUNT[@]}"} \
        "$IMAGE_REF" "${migrate_args[@]}" \
        || die "migrate up 失败，已停止部署（容器未替换，服务仍运行旧版本）"
    log "迁移完成"
fi

# -----------------------------------------------------------------------------
# 6. 原子替换容器
# -----------------------------------------------------------------------------
log "停止并移除旧容器 ${CONTAINER_NAME}"
docker stop "$CONTAINER_NAME" >/dev/null 2>&1 || true
docker rm "$CONTAINER_NAME" >/dev/null 2>&1 || true

start_container() {
    local image="$1"
    local -a runtime_env=()
    [ -n "$CANDIDATE_BASE_URL" ] && runtime_env+=( -e "CANDIDATE_BASE_URL=${CANDIDATE_BASE_URL}" )
    [ -n "$ADMIN_BASE_URL" ] && runtime_env+=( -e "ADMIN_BASE_URL=${ADMIN_BASE_URL}" )
    docker run -d \
        --name "$CONTAINER_NAME" \
        --restart unless-stopped \
        --env-file "$ENV_FILE" \
        "${runtime_env[@]}" \
        -p "${HOST_BIND}:${HOST_PORT}:8080" \
        "$image" >/dev/null
}

log "启动新容器（${IMAGE_REF}，监听 ${HOST_BIND}:${HOST_PORT}）"
if ! start_container "$IMAGE_REF"; then
    warn "新容器启动失败"
    if [ -n "$previous_image_id" ]; then
        log "尝试恢复旧镜像 ${previous_image_id}"
        start_container "$previous_image_id" || die "旧镜像也无法启动，服务当前不可用，请立即人工介入"
        die "新镜像启动失败，已恢复旧镜像"
    fi
    die "首次部署的新镜像启动失败，没有可回滚的旧镜像"
fi

# -----------------------------------------------------------------------------
# 7. 健康门禁 + 回滚
# -----------------------------------------------------------------------------
wait_healthy() {
    local deadline=$((SECONDS + HEALTH_TIMEOUT)) status
    while [ "$SECONDS" -lt "$deadline" ]; do
        status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$CONTAINER_NAME" 2>/dev/null || echo missing)"
        case "$status" in
            healthy) return 0 ;;
            none|missing) return 0 ;;   # 镜像无 HEALTHCHECK 时不阻塞（本镜像有）
            unhealthy)
                log "容器健康检查失败，最近日志："
                docker logs --tail 40 "$CONTAINER_NAME" 2>&1 || true
                return 1
                ;;
        esac
        sleep 3
    done
    log "等待健康检查超时（${HEALTH_TIMEOUT}s），最近日志："
    docker logs --tail 40 "$CONTAINER_NAME" 2>&1 || true
    return 1
}

if wait_healthy; then
    printf '%s\n' "$IMAGE_TAG" > "$CURRENT_TAG_FILE"
    docker image prune -f >/dev/null 2>&1 || true
    log "部署成功：${IMAGE_REF}（当前版本已记录到 ${CURRENT_TAG_FILE}）"
    exit 0
fi

# ---- 回滚 ----
warn "新版本未通过健康门禁，开始回滚"
docker stop "$CONTAINER_NAME" >/dev/null 2>&1 || true
docker rm "$CONTAINER_NAME" >/dev/null 2>&1 || true

if [ -z "$previous_image_id" ]; then
    die "没有可回滚的旧镜像（首次部署），容器已停止，请人工介入排查"
fi

log "回滚到旧镜像 ${previous_image_id}"
start_container "$previous_image_id"

if wait_healthy; then
    die "新版本 ${IMAGE_REF} 健康检查未通过，已回滚到上一个镜像。数据库迁移可能已执行，请人工核对后再决定是否回退数据"
fi
die "回滚后的旧镜像同样未通过健康检查，服务当前不可用，请立即人工介入"
