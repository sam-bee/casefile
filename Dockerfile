FROM node:24-alpine AS web-build

WORKDIR /src/frontend
RUN mkdir -p /src/frontend /tmp/npm && chown -R 1000:1984 /src /tmp/npm
USER 1000:1984
ENV HOME=/tmp NPM_CONFIG_CACHE=/tmp/npm
COPY --chown=1000:1984 frontend/package*.json ./
RUN if [ -f package-lock.json ]; then npm ci; else npm install; fi
COPY --chown=1000:1984 frontend/ ./
RUN npm run build

FROM golang:1.25-alpine AS go-build

WORKDIR /src/service
COPY service/go.mod service/go.sum ./
RUN go mod download
COPY service/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/casefile .

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && addgroup -g 1984 app \
    && adduser -D -u 1000 -G app app
WORKDIR /app
COPY --from=go-build /out/casefile /app/casefile
COPY --from=web-build /src/frontend/dist /app/public
USER 1000:1984
EXPOSE 8080
CMD ["/app/casefile"]
