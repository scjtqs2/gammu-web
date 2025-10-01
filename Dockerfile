# 编译前端内容
FROM node:18-alpine AS front
WORKDIR /build
COPY ./ .
RUN cd src-web && \
    npm install -g vite && \
    npm install && \
    vite build

FROM golang:1.24-alpine3.22 AS builder
#RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories
#RUN go env -w GOPROXY="http://goproxy.cn,direct"

WORKDIR /build
COPY --from=front /build /build
RUN apk add build-base gammu-dev
RUN CGO_ENABLED=1 go build -ldflags "-s -w" -o gammu-web

FROM alpine:3.22 AS production

#RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories
RUN apk add --no-cache gammu bash
# 设置上海时区
RUN apk add --no-cache tzdata && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone && \
    apk del tzdata

COPY --from=builder /build/gammu-web /app/
COPY docker /docker

ENV FORWARD_ENABLED="0"
ENV FORWARD_URL="http://forwardsms:8080/api/v1/sms/receive"
ENV FORWARD_SECRET=""
ENV FORWARD_TIMEOUT="30"
ENV DB_PATH="/data/db"
ENV LOG_PATH="/data/log/gammu-web.log"
ENV USB_PORT="/dev/ttyUSB3"
ENV ATCONNECTION="at115200"
ENV PHONE_ID=""
ENV TZ="Asia/Shanghai"

ENTRYPOINT ["/docker/docker-entrypoint.sh"]

CMD ["/app/gammu-web","-gammu-conf","/docker/gammu.conf"]