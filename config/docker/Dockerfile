FROM golang:1.25-alpine

RUN apk add --no-cache git ca-certificates tzdata curl bash build-base

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go install github.com/air-verse/air@latest
ENV PATH="/go/bin:${PATH}"

EXPOSE 5001 5222

CMD ["air", "-c", ".air.toml"]
