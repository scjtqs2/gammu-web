#!/bin/bash

# 短信转发脚本 - 专用于文件方式的 Gammu 配置
# 环境变量配置:
# FORWARD_URL: 转发服务URL (默认: http://forwardsms:8080)
# FORWARD_SECRET: 可选认证密钥

LOG_FILE="/data/log/forward.log"
INBOX_DIR="/data/sms/inbox"
PROCESSED_DIR="/data/sms/processed"
LOCK_FILE="/tmp/sms_forward.lock"

# 创建必要的目录
mkdir -p "$(dirname "$LOG_FILE")"
mkdir -p "$PROCESSED_DIR"
mkdir -p "$INBOX_DIR"

# 记录日志函数 - 同时输出到文件和控制台
log() {
    local message="$(date '+%Y-%m-%d %H:%M:%S') - $1"
    echo "$message" >> "$LOG_FILE"
#    echo "$message"
}

# 内部调试日志函数 - 确保不干扰函数返回值
log_debug_internal() {
    local message="$1"
    if [ "${DEBUG_SMS:-false}" = "true" ]; then
        local debug_message="$(date '+%Y-%m-%d %H:%M:%S') - $message"
        echo "$debug_message" >> "$LOG_FILE"
#        echo "$debug_message"
    fi
}

# 文件锁函数，防止并发执行
acquire_lock() {
    local timeout=30
    local start_time=$(date +%s)

    while [ -f "$LOCK_FILE" ]; do
        local current_time=$(date +%s)
        local elapsed=$((current_time - start_time))

        if [ $elapsed -gt $timeout ]; then
            log "❌ 获取锁超时，跳过处理"
            return 1
        fi
        sleep 1
    done

    touch "$LOCK_FILE"
    return 0
}

release_lock() {
    rm -f "$LOCK_FILE"
}

# 从环境变量读取配置
FORWARD_URL="${FORWARD_URL:-http://forwardsms:8080/api/v1/sms/receive}"
FORWARD_SECRET="${FORWARD_SECRET:-}"
FORWARD_TIMEOUT="${FORWARD_TIMEOUT:-30}"
PHONE_ID="${PHONE_ID:-default-phone}"

# 安全的调试函数：查看文件实际内容（不产生标准输出）
debug_file_content() {
    local file="$1"

    # 使用子shell和重定向来确保不污染标准输出
    (
        log_debug_internal "🔍 调试文件内容: $file"
        log_debug_internal "   文件大小: $(wc -c < "$file") 字节"
        log_debug_internal "   文件行数: $(wc -l < "$file") 行"
        log_debug_internal "   文件内容（原始）:"

        # 将hexdump输出重定向到文件，然后通过log_debug_internal输出
        local hexdump_output
        hexdump_output=$(hexdump -C "$file" | head -10 2>/dev/null)
        while IFS= read -r line; do
            log_debug_internal "   $line"
        done <<< "$hexdump_output"

        log_debug_internal "   文件内容（文本）:"
        # 将文件内容重定向到文件，然后通过log_debug_internal输出
        local file_content
        file_content=$(cat "$file" 2>/dev/null)
        while IFS= read -r line; do
            log_debug_internal "      $line"
        done <<< "$file_content"
    ) >/dev/null 2>&1  # 确保子shell不产生任何标准输出
}

# 调试函数：显示文件名解析详情
debug_filename_parse() {
    local filename="$1"
    log_debug_internal "🔍 解析文件名: $filename"

    # 显示文件名各部分
    if [[ "$filename" =~ (IN[0-9]{8}_[0-9]{6}_[0-9]{2}_)([^_]+)(_.+\.txt) ]]; then
        local prefix="${BASH_REMATCH[1]}"
        local potential_number="${BASH_REMATCH[2]}"
        local suffix="${BASH_REMATCH[3]}"

        log_debug_internal "   文件名结构:"
        log_debug_internal "   前缀: $prefix"
        log_debug_internal "   可能号码: $potential_number"
        log_debug_internal "   后缀: $suffix"
    else
        log_debug_internal "   ⚠️ 无法解析文件名结构"
    fi
}

# 解析短信文件内容 - 完全安全的版本
parse_sms_file() {
    local file="$1"

    # Gammu 文件格式通常包含这些字段
    local number=""
    local text=""
    local time=""
    local sms_id=""

    # 从文件名提取信息
    sms_id=$(basename "$file")

    # 从文件名解析时间 (格式: IN20251001_193056_00_+8618628287642_00.txt)
    if [[ "$sms_id" =~ IN([0-9]{8})_([0-9]{6}) ]]; then
        local date_part="${BASH_REMATCH[1]}"
        local time_part="${BASH_REMATCH[2]}"
        time="${date_part:0:4}-${date_part:4:2}-${date_part:6:2} ${time_part:0:2}:${time_part:2:2}:${time_part:4:2}"
        log_debug_internal "从文件名解析时间: $time"
    else
        log_debug_internal "⚠️ 无法从文件名解析时间"
    fi

    # 从文件名解析号码
    if [[ "$sms_id" =~ _([+0-9]{11,})_ ]]; then
        number="${BASH_REMATCH[1]}"
        log_debug_internal "从文件名解析到号码: $number"
    else
        # 如果没匹配到，尝试其他模式
        if [[ "$sms_id" =~ _([0-9]{5,})_ ]]; then
            number="${BASH_REMATCH[1]}"
            log_debug_internal "从文件名解析到备用号码: $number"
        else
            log_debug_internal "⚠️ 无法从文件名解析号码"
        fi
    fi

    # 读取文件内容
    if [ -f "$file" ]; then
        # 调试：查看文件实际内容（仅在调试模式下）
        if [ "${DEBUG_SMS:-false}" = "true" ]; then
            debug_file_content "$file"
        fi

        # 读取整个文件内容作为短信正文
        text=$(cat "$file" | tr -d '\r')

        # 如果从文件名中没解析出正确的号码，尝试其他方法
        if [ -z "$number" ] || [ ${#number} -lt 11 ]; then
            log_debug_internal "⚠️ 从文件名解析的号码可能不正确: '$number'，尝试重新解析"
            # 重新从文件名解析，使用更精确的正则
            if [[ "$sms_id" =~ _(\+[0-9]{11,})_ ]]; then
                number="${BASH_REMATCH[1]}"
                log_debug_internal "重新解析到号码: $number"
            elif [[ "$sms_id" =~ _([0-9]{11,})_ ]]; then
                number="${BASH_REMATCH[1]}"
                log_debug_internal "重新解析到号码: $number"
            else
                log_debug_internal "❌ 无法重新解析到有效号码"
            fi
        fi

        # 如果没有明确的时间，使用文件修改时间
        if [ -z "$time" ]; then
            time=$(date -r "$file" "+%Y-%m-%d %H:%M:%S")
            log_debug_internal "使用文件修改时间: $time"
        fi

        # 返回解析结果（只返回数据，不包含日志）
        echo "$sms_id|$number|$text|$time"
    else
        log "❌ 文件不存在: $file"
        echo ""
    fi
}

# 构建 JSON 数据并转发到 Go 服务
forward_to_golang_service() {
    local sms_id="$1"
    local text="$2"
    local number="$3"
    local time="$4"

    # 构建 JSON 数据
    local json_data
    json_data=$(cat <<EOF
{
    "secret": "${FORWARD_SECRET}",
    "number": "${number}",
    "time": "${time}",
    "text": "${text}",
    "source": "gammu-smsd",
    "phone_id": "${PHONE_ID}",
    "sms_id": "${sms_id}",
    "timestamp": "$(date -Iseconds)"
}
EOF
    )

    log "尝试转发短信: $sms_id 到: $FORWARD_URL"
    log "短信数据 - 发件人: $number, 时间: $time, 内容长度: ${#text} 字符"

    # 在调试模式下显示短信内容
    if [ "${DEBUG_SMS:-false}" = "true" ] && [ ${#text} -lt 100 ]; then
        log "短信内容: $text"
    elif [ "${DEBUG_SMS:-false}" = "true" ]; then
        log "短信内容（前100字符）: ${text:0:100}..."
    fi

    # 使用 curl 发送 POST 请求
    local response
    response=$(curl -s -w "\n%{http_code}" \
        -X POST \
        -H "Content-Type: application/json" \
        -H "User-Agent: Gammu-SMSD/1.0" \
        -d "$json_data" \
        --connect-timeout 10 \
        -H "X-Forward-Secret: ${FORWARD_SECRET}" \
        --max-time "$FORWARD_TIMEOUT" \
        "$FORWARD_URL")

    local http_code
    http_code=$(echo "$response" | tail -n1)
    local response_body
    response_body=$(echo "$response" | head -n -1)

    if [ "$http_code" -eq 200 ]; then
        log "✓ 短信: $sms_id 成功转发到 Go 服务 (HTTP $http_code)"
        if [ -n "$response_body" ]; then
            log "  服务响应: $response_body"
        fi
        return 0
    else
        log "✗ 短信: $sms_id 转发失败 - HTTP 状态码: $http_code"
        if [ -n "$response_body" ]; then
            log "  错误响应: $response_body"
        fi
        return 1
    fi
}

# 处理单条短信文件
process_single_sms() {
    local file="$1"

    # 调试文件名解析
    if [ "${DEBUG_SMS:-false}" = "true" ]; then
        debug_filename_parse "$(basename "$file")"
    fi

    # 解析短信文件 - 使用临时文件避免命令替换问题
    local temp_output
    temp_output=$(mktemp)
    parse_sms_file "$file" > "$temp_output" 2>/dev/null
    local parsed_data
    parsed_data=$(cat "$temp_output")
    rm -f "$temp_output"

    if [ -z "$parsed_data" ]; then
        log "❌ 无法解析短信文件: $file"
        return 1
    fi

    # 提取解析的数据
    IFS='|' read -r sms_id number text time <<< "$parsed_data"

    log "处理短信: $sms_id - 发件人: $number, 时间: $time"

    # 检查号码是否有效（至少11位）
    if [ -z "$number" ] || [ ${#number} -lt 11 ]; then
        log "⚠️ 号码格式可能无效: '$number'，长度: ${#number}"
        # 但继续处理，因为可能是短号码
    fi

    # 转义 JSON 特殊字符
    local escaped_text=$(echo "$text" | sed 's/"/\\"/g' | sed 's/\\/\\\\/g')

    # 转发到 Go 服务
    if forward_to_golang_service "$sms_id" "$escaped_text" "$number" "$time"; then
        # 只有转发成功才移动文件
        mv "$file" "$PROCESSED_DIR/"
        log "✅ 短信: $sms_id 处理完成，文件已移动到 processed 目录"
        return 0
    else
        log "❌ 短信: $sms_id 转发失败，保留文件等待重试"
        return 1
    fi
}

# 获取未处理的短信文件
get_unprocessed_sms_files() {
    # 查找 inbox 目录下所有文件，按修改时间排序
    find "$INBOX_DIR" -maxdepth 1 -type f -name "IN*.txt" | sort
}

# 主函数
main() {
    log "📱 开始检查未处理短信..."
    log "环境配置 - FORWARD_URL: $FORWARD_URL, PHONE_ID: $PHONE_ID, DEBUG_SMS: ${DEBUG_SMS:-false}"

    # 检查 inbox 目录是否存在
    if [ ! -d "$INBOX_DIR" ]; then
        log "❌ Inbox 目录不存在: $INBOX_DIR"
        exit 1
    fi

    # 获取文件锁，防止并发执行
    if ! acquire_lock; then
        log "⚠️ 已有处理进程在运行，退出"
        exit 0
    fi

    # 确保锁被释放
    trap release_lock EXIT

    # 获取未处理的短信文件
    local unprocessed_files
    unprocessed_files=$(get_unprocessed_sms_files)

    if [ -z "$unprocessed_files" ]; then
        log "ℹ️ 没有未处理的短信"
        release_lock
        exit 0
    fi

    # 统计处理数量
    local processed_count=0
    local failed_count=0

    # 处理每一个短信文件
    while IFS= read -r file; do
        if [ -f "$file" ]; then
            if process_single_sms "$file"; then
                processed_count=$((processed_count + 1))
            else
                failed_count=$((failed_count + 1))
                log "⚠️ 短信文件处理失败: $file，继续处理下一个"
            fi
        fi
    done <<< "$unprocessed_files"

    log "📊 处理完成 - 成功: $processed_count, 失败: $failed_count"
}

# 执行主函数
main "$@"