FROM golang:1.24-alpine3.22 AS builder
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories
RUN go env -w GOPROXY="http://goproxy.cn,direct"

WORKDIR /build
COPY ./ .
RUN apk add build-base gammu-dev
RUN CGO_ENABLED=1 go build -ldflags "-s -w" -o gammu-web

FROM alpine:3.22 AS production

RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories
RUN apk add --no-cache gammu

COPY --from=builder /build/gammu-web /app/
COPY docker /docker

ENV FORWARD_ENABLED="0"
ENV FORWARD_URL="http://forwardsms:8080/api/v1/sms/receive"
ENV FORWARD_SECRET=""
ENV FORWARD_TIMEOUT="30"

ENTRYPOINT ["/docker/docker-entrypoint.sh"]

CMD ["/app/gammu-web","-c","/docker/gammu.conf"]