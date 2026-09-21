FROM golang:1.24.5  AS builder
RUN go env -w GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY pkg ./pkg
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/server ./cmd

FROM alpine:latest
# 安装ca证书，解决https调用失败
#RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories && \
#    apk add --no-cache ca-certificates tzdata


COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
ENV TZ=Asia/Shanghai


RUN mkdir -p /app/www
COPY --from=builder /out/server /app/server
EXPOSE 7080
CMD ["/app/server", "--frontend-path", "/app/www", "--openapi-key", "sk-cdeeaa28d90e4ad990be0cf010132cdf", "--openai-model", "deepseek-v4-flash", "--kv-store-master-url", "192.168.60.146:2000", "--kv-store-slave-url", "192.168.60.147:2001", "--embedding-url", "http://chat-embedding:8001"]
