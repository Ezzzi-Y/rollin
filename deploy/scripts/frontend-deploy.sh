#!/usr/bin/env bash
# =============================================================================
# Rollin 前端部署脚本（在目标服务器上执行，不涉及 Docker）
# =============================================================================
# 由 .github/workflows/deploy-frontend.yml 通过 SSH 调用，也可以登录服务器后手动执行。
#
# 做的事，按顺序：
#   1. 前置检查：归档文件、目录权限，并**先做一次 nginx -t**（配置有问题就不动手）
#      若传入 NGINX_CONFIG，则先备份并安装本次渲染出的站点配置，再验证它。
#   2. 解压到临时目录并做产物完整性检查（index.html 必须存在）
#   3. 原子放入 releases/<release>/
#   4. 原子切换 current 符号链接（ln + mv -T，不存在半成品窗口）
#   5. reload nginx；失败则把符号链接切回上一版本
#   6. 清理超出保留数量的历史版本
#
# 顺序说明：nginx -t 放在改动之前，是为了避免「配置本来就有问题」导致一次正常的前端发布
# 被误判为失败而回滚。前端发布并不修改 nginx 配置，真正需要验证的是 reload 能否成功。
#
# 权限说明：部署流水线使用 root SSH 用户，因此脚本直接执行文件操作、`nginx -t` 和 reload。
#
# 目录约定：
#   RELEASES_DIR   每个版本一份产物，例如 /opt/rollin/releases/sha-1a2b3c4/
#   WEB_ROOT       nginx 的 root，指向 current 符号链接，例如 /var/www/rollin/current
#
# 输入（环境变量）：
#   RELEASE        必填  版本标识，例如 sha-1a2b3c4
#   ARCHIVE        必填  服务器上已上传的 tar.gz 路径
#   WEB_ROOT       默认 /var/www/rollin/current
#   RELEASES_DIR   默认 /opt/rollin/releases
#   KEEP           默认 5    保留最近几个历史版本
#   NGINX_RELOAD   默认 1    置 0 则只落盘、不 reload
#   NGINX_CONFIG   可选  本次要安装的宿主机 nginx 配置文件
#   NGINX_SITE     默认 /etc/nginx/conf.d/rollin.conf
# =============================================================================
set -Eeuo pipefail

RELEASE="${RELEASE:?必须提供 RELEASE（版本标识，例如 sha-1a2b3c4）}"
ARCHIVE="${ARCHIVE:?必须提供 ARCHIVE（服务器上已上传的 tar.gz 路径）}"
WEB_ROOT="${WEB_ROOT:-/var/www/rollin/current}"
RELEASES_DIR="${RELEASES_DIR:-/opt/rollin/releases}"
KEEP="${KEEP:-5}"
NGINX_RELOAD="${NGINX_RELOAD:-1}"
NGINX_CONFIG="${NGINX_CONFIG:-}"
NGINX_SITE="${NGINX_SITE:-/etc/nginx/conf.d/rollin.conf}"

log()  { printf '[frontend] %s\n' "$*"; }
warn() { printf '[frontend][WARN] %s\n' "$*" >&2; }
die()  { printf '[frontend][ERROR] %s\n' "$*" >&2; exit 1; }

on_error() { die "脚本在第 ${1} 行失败，部署中断"; }
trap 'on_error $LINENO' ERR

case "$KEEP" in ''|*[!0-9]*) die "KEEP 必须是非负整数，当前值：${KEEP}" ;; esac

# -----------------------------------------------------------------------------
# 1. 前置检查（此阶段不修改任何线上内容）
# -----------------------------------------------------------------------------
log "开始部署前端版本 ${RELEASE}"

[ -f "$ARCHIVE" ] || die "归档文件不存在：${ARCHIVE}"
[ -s "$ARCHIVE" ] || die "归档文件为空：${ARCHIVE}"
[ -z "$NGINX_CONFIG" ] || { [ -f "$NGINX_CONFIG" ] || die "nginx 配置文件不存在：${NGINX_CONFIG}"; }

has_nginx=0
command -v nginx >/dev/null 2>&1 && has_nginx=1

[ "$has_nginx" = "1" ] || die "服务器上没有 nginx，前端部署无法继续"

nginx_site_backup=""
nginx_site_changed=0
restore_nginx_site() {
    [ "$nginx_site_changed" = "1" ] || return 0
    if [ -n "$nginx_site_backup" ] && [ -f "$nginx_site_backup" ]; then
        cp -f "$nginx_site_backup" "$NGINX_SITE"
        rm -f "$nginx_site_backup"
    else
        rm -f "$NGINX_SITE"
    fi
    nginx_site_changed=0
}

if [ "$has_nginx" = "1" ] && [ -n "$NGINX_CONFIG" ]; then
    if [ -f "$NGINX_SITE" ]; then
        nginx_site_backup="${NGINX_SITE}.bak.$$.${RELEASE}"
        cp -f "$NGINX_SITE" "$nginx_site_backup"
    fi
    install -D -m 0644 "$NGINX_CONFIG" "$NGINX_SITE"
    nginx_site_changed=1
fi

if [ "$NGINX_RELOAD" = "1" ] && [ "$has_nginx" = "1" ]; then
    log "校验 nginx 配置（nginx -t）"
    if ! nginx -t; then
        restore_nginx_site
        die "nginx -t 失败，已恢复部署前的站点配置"
    fi
    log "nginx 配置校验通过"
fi

target_dir="${RELEASES_DIR}/${RELEASE}"
mkdir -p "$RELEASES_DIR" "$(dirname "$WEB_ROOT")"

previous_target=""
if [ -L "$WEB_ROOT" ]; then
    previous_target="$(readlink "$WEB_ROOT")"
    log "当前版本符号链接指向：${previous_target}"
elif [ -e "$WEB_ROOT" ]; then
    die "${WEB_ROOT} 已存在且不是符号链接，请先手动处理（避免误删真实目录）"
fi

# -----------------------------------------------------------------------------
# 2. 解压到临时目录 + 产物检查
# -----------------------------------------------------------------------------
staging="$(mktemp -d "${RELEASES_DIR}/.staging-${RELEASE}.XXXXXX")"
cleanup_staging() {
    if [ -d "$staging" ]; then rm -rf -- "$staging"; fi
    return 0
}
trap 'cleanup_staging; on_error $LINENO' ERR

log "解压产物到临时目录"
tar -xzf "$ARCHIVE" -C "$staging"

# 归档约定：根目录直接是 dist 的内容（index.html / assets/ / favicon.svg）
[ -f "${staging}/index.html" ] || die "产物缺少 index.html，归档结构不符合预期"
[ -d "${staging}/assets" ] || warn "产物中未找到 assets/ 目录，请确认 Vite 构建输出是否正常"

# -----------------------------------------------------------------------------
# 3. 原子放入版本目录
# -----------------------------------------------------------------------------
if [ -d "$target_dir" ]; then
    warn "版本目录已存在，先移除重建：${target_dir}"
    rm -rf -- "$target_dir"
fi
mv "$staging" "$target_dir"
trap 'on_error $LINENO' ERR

# nginx worker（通常 www-data）需要可读；只给读/进入权限，不放行组或其他人的写权限
chmod -R a+rX "$target_dir"
log "产物已就位：${target_dir}"

# -----------------------------------------------------------------------------
# 4. 原子切换符号链接
# -----------------------------------------------------------------------------
link_tmp="${WEB_ROOT}.new.$$"

switch_link() {
    ln -sfn "$1" "$link_tmp"
    mv -Tf "$link_tmp" "$WEB_ROOT"
}

switch_link "$target_dir"
log "已切换 ${WEB_ROOT} -> ${target_dir}"

# -----------------------------------------------------------------------------
# 5. reload nginx（失败回滚符号链接）
# -----------------------------------------------------------------------------
if [ "$NGINX_RELOAD" = "1" ] && [ "$has_nginx" = "1" ]; then
    reload_nginx() {
        if command -v systemctl >/dev/null 2>&1; then
            systemctl reload nginx
        else
            nginx -s reload
        fi
    }

    if reload_nginx; then
        log "nginx 已 reload"
    else
        warn "nginx reload 失败"
        if [ -n "$previous_target" ] && [ -d "$previous_target" ]; then
            log "回滚符号链接到 ${previous_target}"
            switch_link "$previous_target"
            reload_nginx || warn "回滚后 reload 仍未成功，请人工介入检查 nginx"
        else
            warn "没有可回滚的上一版本（首次部署），请人工介入检查 nginx"
        fi
        restore_nginx_site
        reload_nginx || warn "恢复旧站点配置后的 nginx reload 仍未成功"
        die "nginx reload 失败，前端版本和站点配置已回滚"
    fi
elif [ "$NGINX_RELOAD" != "1" ]; then
    warn "NGINX_RELOAD=0，跳过 reload（产物已落盘但不会生效）"
else
    warn "服务器未找到 nginx 命令，跳过 reload（若 nginx 跑在容器里请自行处理）"
fi

# -----------------------------------------------------------------------------
# 6. 清理历史版本
# -----------------------------------------------------------------------------
# shellcheck disable=SC2012
ls -1dt "${RELEASES_DIR}"/*/ 2>/dev/null | tail -n +"$((KEEP + 1))" | while read -r old; do
    # 绝不删除 current 指向的版本，也不删除上一版本（保留一次手工回滚的余地）
    if [ -n "$previous_target" ] && [ "$(readlink -f "$old")" = "$(readlink -f "$previous_target" 2>/dev/null || echo -n "$previous_target")" ]; then
        continue
    fi
    if [ "$(readlink -f "$WEB_ROOT")" = "$(readlink -f "$old")" ]; then
        continue
    fi
    log "清理历史版本 $(basename "$old")"
    rm -rf -- "$old"
done || true

rm -f -- "$ARCHIVE" 2>/dev/null || warn "未能删除上传的归档文件：${ARCHIVE}"

log "前端部署成功：${RELEASE}"
if [ "$nginx_site_changed" = "1" ] && [ -n "$nginx_site_backup" ]; then
    rm -f -- "$nginx_site_backup"
fi
