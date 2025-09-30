#!/bin/bash

set -e

# 替换 USB 端口配置
if [ -n "$USB_PORT" ]; then
    echo "配置 USB 端口: $USB_PORT"
    sed -i "s|%USB_PORT%|$USB_PORT|g" /docker/gammu.conf
else
    echo "警告: USB_PORT 环境变量未设置，使用默认配置"
fi

if [ -n "$ATCONNECTION" ]; then
   echo "配置 AT 连接: $ATCONNECTION"
   sed -i "s|%ATCONNECTION%|$ATCONNECTION|g" /docker/gammu.conf
else
    echo "警告: ATCONNECTION 环境变量未设置，使用默认配置"
fi

# 创建必要的目录
mkdir -p /data/log /data/db /var/log/gammu

# 检查配置文件是否存在
if [ ! -f "/docker/gammu.conf" ]; then
    echo "错误: /docker/gammu.conf 配置文件不存在"
    exit 1
fi

# 显示配置信息
echo "=== Gammu SMSD 配置信息 ==="
echo "USB 端口: ${USB_PORT:-未设置}"
echo "是否开启转发: ${FORWARD_ENABLED:-未设置}"
echo "转发 URL: ${FORWARD_URL:-未设置}"
echo "AT 连接: ${ATCONNECTION:-未设置}"
echo "数据库路径: ${DB_PATH:-/data/db}${DB_NAME:-/gammu.db}"
echo "日志路径 ${LOG_PATH:-未设置}"
echo "=========================="

# 执行传入的命令
exec "$@"