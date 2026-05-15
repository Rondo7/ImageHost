FROM golang:1.22-alpine AS builder

RUN apk add --no-cache gcc musl-dev libwebp-dev && \
    rm -rf /var/cache/apk/*

WORKDIR /app

# 只拷 go.mod，在容器内执行 tidy 自动生成 go.sum
COPY go.mod ./
COPY . .
RUN go mod tidy && \
    CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o imagehost . && \
    strip imagehost

# 最终镜像：从 builder 直接复制 .so，跳过 apk add
FROM alpine:3.19

COPY --from=builder /usr/lib/libwebp.so* /usr/lib/
COPY --from=builder /usr/lib/libsharpyuv.so* /usr/lib/

WORKDIR /app
COPY --from=builder /app/imagehost .
COPY frontend/ ./frontend/

RUN mkdir -p uploads/original uploads/webp uploads/gif

EXPOSE 8080

ENV UPLOAD_PASSWORD=admin123
ENV PORT=8080

CMD ["./imagehost"]
