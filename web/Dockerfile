# Сборка SPA и раздача через nginx (ADR 015). nginx отдаёт статику и
# проксирует /v1 в api: браузер работает с одним origin, без CORS.
FROM node:24-alpine AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
RUN npm run build

FROM nginx:1.29-alpine
# Шаблон конфигурации: переменные подставляет entrypoint образа (envsubst).
COPY nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /src/dist /usr/share/nginx/html
ENV API_UPSTREAM=http://api:8080 \
    MEDIA_ORIGIN=http://localhost:8333 \
    NGINX_ENTRYPOINT_LOCAL_RESOLVERS=1
EXPOSE 80
HEALTHCHECK --interval=10s --timeout=3s --retries=5 CMD wget -q -O /dev/null http://127.0.0.1/healthz || exit 1
