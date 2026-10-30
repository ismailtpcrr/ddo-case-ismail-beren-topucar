# syntax=docker/dockerfile:1

# --- 1. aşama: derleme ---
FROM node:24-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --no-audit --no-fund
COPY . .
RUN npm run build

# --- 2. aşama: sunum ---
# nginx-unprivileged: root olmayan kullanıcıyla (uid 101) çalışır ve 8080'i dinler.
# Çalışma imajında Node yok; yalnız derlenmiş statik dosyalar ve nginx.
FROM nginxinc/nginx-unprivileged:1.30-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 8080
