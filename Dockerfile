# Stage 1: Build frontend
FROM node:22-alpine AS frontend-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit 2>/dev/null || npm install --no-audit
COPY web/ ./
RUN npm run build

# Stage 2: Build Go binary
FROM golang:1.25-alpine AS go-builder
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=frontend-builder /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o syncwatch .

# Stage 3: Runtime
FROM alpine:3.21
RUN apk add --no-cache ffmpeg ca-certificates tzdata
COPY --from=go-builder /src/syncwatch /usr/local/bin/syncwatch
EXPOSE 8080
EXPOSE 60000-60100/udp
VOLUME ["/media", "/data"]
ENTRYPOINT ["syncwatch"]
CMD ["--config", "/data/config.yaml"]
