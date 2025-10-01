# 编译前端内容
FROM node:20-alpine AS front
WORKDIR /build
COPY ./ .
RUN cd src-web && \
    npm install -g vite && \
    npm install && \
    npm run build:css && \
    vite build

# 编译 Go 后端
FROM golang:1.25-bookworm AS builder

# 设置国内镜像源（可选）
#RUN sed -i 's/deb.debian.org/mirrors.aliyun.com/g' /etc/apt/sources.list && \
# sed -i 's/security.debian.org/mirrors.aliyun.com/g' /etc/apt/sources.list

# 设置 Go 代理（可选）
# RUN go env -w GOPROXY="https://goproxy.cn,direct"

WORKDIR /build
COPY --from=front /build /build

# 安装 Debian 构建依赖
RUN apt-get update && apt-get install -y \
    build-essential \
    libgammu-dev \
    pkg-config \
    gcc \
    make \
    cmake \
    && rm -rf /var/lib/apt/lists/*

# 编译 Go 程序
RUN CGO_ENABLED=1 go build -ldflags "-s -w" -o gammu-web

# 生产环境
FROM debian:bookworm-slim AS production

# 设置国内镜像源（可选）
#RUN sed -i 's/deb.debian.org/mirrors.aliyun.com/g' /etc/apt/sources.list && \
# sed -i 's/security.debian.org/mirrors.aliyun.com/g' /etc/apt/sources.list

# 安装运行时依赖
RUN apt-get update && apt-get install -y \
    libgammu-dev \
    gammu \
    gammu-smsd \
    bash \
    ca-certificates \
    curl \
    jq \
    tzdata \
    && rm -rf /var/lib/apt/lists/*

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

ENTRYPOINT ["/docker/docker-entrypoint.sh"]

CMD ["/app/gammu-web","-gammu-conf","/docker/gammu.conf"]