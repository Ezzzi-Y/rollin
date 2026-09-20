# =============================================================================
# Rollin 前端镜像（多阶段）：node 构建 Vite 产物 → nginx 静态交付
#
# API 同源约定：VITE_API_BASE 默认为空，前端所有 /api 请求发往自身域名，
# 由边缘 nginx（deploy/nginx.conf.example）按域名白名单反代到后端容器。
# 若需指向独立 API 域名（跨域），构建时注入，例如：
#   docker build --build-arg VITE_API_BASE=https://api.example.edu.cn -t rollin-frontend .
#   （注意：后端未实现 CORS 中间件，跨域 API 域名需自行在边缘层补齐 CORS 头）
#
# 构建：docker build -t rollin-frontend:latest ./rollin-frontend
# =============================================================================
FROM node:22-alpine AS builder

WORKDIR /src
# 先拷依赖清单并安装，源码变更不击穿依赖层缓存
COPY package.json package-lock.json ./
RUN npm ci
COPY . .

# VITE_* 变量在构建期注入打包产物，运行时不可再改；默认空 = 同源
ARG VITE_API_BASE=""
ENV VITE_API_BASE=$VITE_API_BASE

RUN npm run build

# -----------------------------------------------------------------------------
# 静态交付：SPA fallback 见 nginx.conf（/o/:token 深链直开依赖它）
# -----------------------------------------------------------------------------
FROM nginx:1.28-alpine

COPY --from=builder /src/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 80

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q --spider http://127.0.0.1/ || exit 1
