# 编译前端内容
FROM node:20-alpine AS front
WORKDIR /build
COPY ./ .
# 设置 npm 国内镜像（简化版）
# RUN npm config set registry https://registry.npmmirror.com
RUN cd src-web && \
    npm install -g vite && \
    npm install && \
    npm run build:css && \
    vite build

# 编译 Go 后端
FROM golang:1.25-alpine3.22 AS builder

# 设置 Alpine 镜像源（可选）
# RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories
# 设置 Go 代理（可选）
# RUN go env -w GOPROXY="https://goproxy.cn,direct"
WORKDIR /build
COPY --from=front /build /build

# 安装 Alpine 构建依赖
RUN apk add --no-cache \
    build-base \
    gammu-dev \
    pkgconfig \
    gcc \
    make \
    cmake

# 编译 Go 程序
RUN CGO_ENABLED=1 go build -ldflags "-s -w" -o gammu-web

# 生产环境
FROM alpine:3.22 AS production

# 设置 Alpine 镜像源（可选）
# RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories

# 安装运行时依赖
RUN apk add --no-cache \
    gammu-libs \
    bash \
    ca-certificates \
    tzdata

# 设置上海时区
RUN ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone

# 复制编译好的程序
COPY --from=builder /build/gammu-web /app/
COPY docker /docker
RUN chmod +x /docker/*.sh

# 创建必要的目录
RUN mkdir -p /data/db /data/log

# 环境变量
ENV FORWARD_ENABLED="0"
ENV FORWARD_URL="http://forwardsms:8080/api/v1/sms/receive"
ENV FORWARD_SECRET=""
ENV FORWARD_TIMEOUT="30"
ENV DB_PATH="/data/db"
ENV LOG_PATH="/data/log/gammu-web.log"
ENV USB_PORT="/dev/ttyUSB2"
ENV ATCONNECTION="at115200"
ENV PHONE_ID=""
ENV TZ="Asia/Shanghai"
# 日志debug级别
ENV DEBUG="0"
# 打印gammu的debug日志
ENV GAMMU_DEBUG="0"

ENTRYPOINT ["/docker/docker-entrypoint.sh"]

CMD ["/app/gammu-web","-gammu-conf","/docker/gammu.conf"]