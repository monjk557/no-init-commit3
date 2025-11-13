FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata curl bash build-base

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 빌드 단계: 바이너리 빌드
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o git-executor ./cmd/git-executor

# 런타임 이미지
FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata curl git

WORKDIR /app

# 빌더에서 빌드된 바이너리 복사
COPY --from=builder /app/git-executor .

EXPOSE 5001 5222

# 빌드된 바이너리 직접 실행
CMD ["./git-executor"]
