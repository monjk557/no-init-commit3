FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata curl bash build-base

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 빌드 단계: 바이너리 빌드
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o git-executor ./cmd/git-executor

# Specs 서버 이미지 (nginx 기반) - 명시적으로 타겟 지정 필요
FROM nginx:alpine AS specs

# Copy nginx configuration
COPY config/docker/specs/nginx.conf /etc/nginx/conf.d/default.conf

# Copy OpenAPI spec file
COPY specs/generated/collabops-git-executor.yaml /usr/share/nginx/html/openapi.yaml
COPY specs/generated/collabops-git-executor.yaml /usr/share/nginx/html/swagger.yaml
COPY specs/generated/collabops-git-executor.yaml /usr/share/nginx/html/collabops-git-executor.yaml

# Create a simple index page that redirects to the spec
RUN echo '<!DOCTYPE html><html><head><meta http-equiv="refresh" content="0; url=/openapi.yaml"><title>OpenAPI Spec</title></head><body><p>Redirecting to <a href="/openapi.yaml">OpenAPI Specification</a></p></body></html>' > /usr/share/nginx/html/index.html

# Expose port 4080
EXPOSE 4080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --quiet --tries=1 --spider http://localhost/health || exit 1

# Start nginx
CMD ["nginx", "-g", "daemon off;"]

# 애플리케이션 런타임 이미지 (기본 타겟 - 기존 CI/CD와 호환)
FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata curl git

WORKDIR /app

# 빌더에서 빌드된 바이너리 복사
COPY --from=builder /app/git-executor .

# PolicyEngine 설정 파일 복사 (환경 파일 보호용; 지우지마시오!)
COPY --from=builder /app/configs ./configs

EXPOSE 5001 5222

# 빌드된 바이너리 직접 실행
CMD ["./git-executor"]
