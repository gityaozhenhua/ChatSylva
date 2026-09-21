FROM quay.io/0voice/node:lts-alpine AS frontend

WORKDIR /app

# 直接npm全局固定安装 pnpm@10.34.5，和你本地版本完全一致，不装最新版！
RUN npm install -g pnpm@10.34.5

# 拷贝依赖文件
COPY package.json pnpm-lock.yaml ./

# --ignore‑scripts 跳过husky、prepare脚本
RUN pnpm install --ignore-scripts

# 拷贝全部源码
COPY . .

# 前端编译
RUN pnpm run build-only

FROM quay.io/0voice/nginx:1.25.4 as web
# ✅ 复制自定义 Nginx 配置
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=frontend /app/dist/ /usr/share/nginx/html/
EXPOSE 80
ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["nginx", "-g", "daemon off;"]
