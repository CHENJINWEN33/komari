#!/usr/bin/env bash
# setup-mcp-proxy.sh — 一键把 Komari 的 MCP 端点通过 HTTPS 暴露给 AI 客户端
#
#   sudo bash setup-mcp-proxy.sh
#
# 做四件事：
#   1. 在已有的 nginx / Caddy 里新增一个站点（不覆盖任何现有配置）
#   2. 开两个 MCP 入口：
#      A. /api/mcp     —— 客户端自带 Authorization 头（推荐，凭据不进 URL）
#      B. /mcp-<密钥>  —— 由反代代为注入，给填不了请求头的客户端用
#   3. 把 Komari 改为只监听 127.0.0.1，并设置反代场景必需的环境变量
#   4. 验证，并打印可直接粘贴到 AI 客户端的 URL
#
# 设计原则：宁可中止，也不破坏现有配置。所有写入都是新建文件或追加，
# 改动前备份，应用前先做语法校验。

set -euo pipefail

KOMARI_DIR="/opt/komari"
KOMARI_PORT="25774"
SERVICE="komari"
DROPIN_DIR="/etc/systemd/system/${SERVICE}.service.d"

C_RED=$'\033[0;31m'; C_GRN=$'\033[0;32m'; C_YEL=$'\033[0;33m'
C_CYA=$'\033[0;36m'; C_DIM=$'\033[2m';   C_OFF=$'\033[0m'
info()  { echo "${C_CYA}==>${C_OFF} $*"; }
ok()    { echo "${C_GRN} ok ${C_OFF} $*"; }
warn()  { echo "${C_YEL}warn${C_OFF} $*"; }
die()   { echo "${C_RED}错误${C_OFF} $*" >&2; exit 1; }
hint()  { echo "${C_DIM}     $*${C_OFF}"; }

ask() { # ask <提示> <默认值>
    local prompt="$1" def="${2:-}" ans
    if [ -n "$def" ]; then
        read -r -p "$prompt [$def]: " ans </dev/tty || true
        echo "${ans:-$def}"
    else
        read -r -p "$prompt: " ans </dev/tty || true
        echo "$ans"
    fi
}

# ---------- 1. 前置检查 ----------

[ "$(id -u)" -eq 0 ] || die "需要 root：sudo bash $0"
command -v systemctl >/dev/null 2>&1 || die "未检测到 systemd，本脚本只支持 systemd 系统。"
[ -x "$KOMARI_DIR/komari" ] || die "未找到 $KOMARI_DIR/komari，请先用 install-komari.sh 安装 Komari。"
systemctl is-active --quiet "$SERVICE" || warn "komari 服务当前未运行，稍后仍会继续配置。"

# 检测反向代理。两个都装了就让用户选，都没装则中止并给出安装提示。
HAS_NGINX=0; HAS_CADDY=0
command -v nginx >/dev/null 2>&1 && HAS_NGINX=1
command -v caddy >/dev/null 2>&1 && HAS_CADDY=1

if [ "$HAS_NGINX" -eq 1 ] && [ "$HAS_CADDY" -eq 1 ]; then
    info "同时检测到 nginx 和 Caddy。"
    PROXY=$(ask "用哪个？(nginx/caddy)" "nginx")
elif [ "$HAS_NGINX" -eq 1 ]; then
    PROXY="nginx"
elif [ "$HAS_CADDY" -eq 1 ]; then
    PROXY="caddy"
else
    die "未检测到 nginx 或 Caddy。请先装一个再运行本脚本，推荐 Caddy（证书全自动）：
       apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
       curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
       curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' > /etc/apt/sources.list.d/caddy-stable.list
       apt-get update && apt-get install -y caddy"
fi
ok "反向代理：$PROXY"

# 443 被别的程序占用是很常见的情况（xray、其他 Web 服务、Docker 容器等）。
# 必须在动手写配置前就发现，否则用户装完 certbot、跑到一半才在 nginx -t
# 撞墙。占用时改用备用端口——对 MCP 客户端来说非标准端口毫无影响，
# 它只是 URL 里的一个数字。
HTTPS_PORT="443"
port_owner() { # port_owner <端口> -> 打印占用它的进程名
    ss -tlnpH 2>/dev/null | awk -v p=":$1\$" '$4 ~ p {print $0}' \
        | grep -oE 'users:\(\("[^"]+' | grep -oE '"[^"]+' | tr -d '"' | head -1
}
OWNER="$(port_owner 443 || true)"
if [ -n "$OWNER" ] && [ "$OWNER" != "$PROXY" ]; then
    echo
    warn "443 端口已被 ${C_YEL}$OWNER${C_OFF} 占用，$PROXY 无法监听它。"
    hint "常见于同机跑了 xray / 其他站点 / Docker 容器。"
    hint "改用备用端口即可，MCP 客户端不在意端口号；面板地址也会带上这个端口。"
    HTTPS_PORT=$(ask "改用哪个端口？" "25443")
    case "$HTTPS_PORT" in
        ''|*[!0-9]*) die "端口必须是数字。";;
    esac
    [ "$HTTPS_PORT" -ge 1 ] && [ "$HTTPS_PORT" -le 65535 ] || die "端口超出范围：$HTTPS_PORT"
    BUSY="$(port_owner "$HTTPS_PORT" || true)"
    [ -z "$BUSY" ] || die "$HTTPS_PORT 也被 $BUSY 占用了，换一个再试。"
    ok "HTTPS 将监听 $HTTPS_PORT"
    hint "记得在防火墙/云厂商安全组里放行 $HTTPS_PORT"
fi

# ---------- 2. 收集参数 ----------

echo
DOMAIN=$(ask "面板要用的域名（需已解析到本机，例 mon.example.com）")
[ -n "$DOMAIN" ] || die "域名不能为空。"
case "$DOMAIN" in *[!A-Za-z0-9.-]*) die "域名含非法字符：$DOMAIN";; esac

# 解析校验：不一致只警告不中止，DNS 可能还在生效中。
MYIP=$(curl -fsS4 --max-time 8 ifconfig.me 2>/dev/null || true)
RESOLVED=$(getent hosts "$DOMAIN" 2>/dev/null | awk '{print $1}' | head -1 || true)
if [ -n "$MYIP" ] && [ -n "$RESOLVED" ] && [ "$MYIP" != "$RESOLVED" ]; then
    warn "$DOMAIN 解析到 $RESOLVED，本机公网 IP 是 $MYIP —— 不一致会导致证书申请失败。"
    [ "$(ask '仍要继续？(y/N)' 'N')" = "y" ] || exit 1
elif [ -z "$RESOLVED" ]; then
    warn "$DOMAIN 当前解析不到。若刚改过 DNS，等生效后再运行。"
    [ "$(ask '仍要继续？(y/N)' 'N')" = "y" ] || exit 1
fi

# API Key 优先从 Komari 数据库读，读不到再让用户贴。
API_KEY=""
DB="$KOMARI_DIR/data/komari.db"
if [ -f "$DB" ] && command -v python3 >/dev/null 2>&1; then
    API_KEY=$(python3 - "$DB" <<'PY' 2>/dev/null || true
import json, sqlite3, sys
try:
    r = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True).execute(
        "select value from configs where key='api_key'").fetchone()
    raw = (r[0] or "") if r else ""
    # configs 表里的值是 JSON 编码的：字符串带引号存放（'"komari-xxx"'）。
    # 直接拿原始值会把引号一起写进 nginx 配置，生成
    #   proxy_set_header Authorization "Bearer "komari-xxx"";
    # nginx 报 unexpected "k"。所以必须先解码。
    try:
        v = json.loads(raw)
        if not isinstance(v, str):
            v = ""
    except Exception:
        v = raw          # 万一哪天改成明文存储，原样使用
    print(v.strip())
except Exception:
    print("")
PY
    )
fi

if [ -n "$API_KEY" ] && [ "${#API_KEY}" -ge 12 ]; then
    ok "已从数据库读到 API Key（${#API_KEY} 位）"
else
    echo
    warn "数据库里没有可用的 API Key（需 >= 12 位）。"
    hint "请先在 Komari 后台「设置」里生成 API Key 并保存，然后把它贴到下面。"
    API_KEY=$(ask "Komari API Key")
    [ "${#API_KEY}" -ge 12 ] || die "API Key 至少 12 位，Komari 会拒绝更短的值。"
fi

# API Key 会被原样嵌进反代配置的双引号字符串里。含引号、反斜杠、分号或
# 空白都会破坏配置语法（甚至注入额外指令），此处直接拒绝而不是尝试转义——
# Komari 生成的 Key 是 "komari-" 加随机字母数字，不会命中这些字符。
case "$API_KEY" in
    *'"'*|*'\'*|*';'*|*' '*|*"$(printf '\t')"*)
        die "API Key 含引号、反斜杠、分号或空白字符，无法安全写入 $PROXY 配置。
       请在 Komari 后台重新生成一个标准格式的 Key。";;
esac

# URL 密钥：出现在 URL 路径里，等同密码，所以用随机值而非用户输入。
if command -v openssl >/dev/null 2>&1; then
    URLKEY=$(openssl rand -hex 16)
else
    URLKEY=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')
fi
MCP_PATH="/mcp-$URLKEY"

# ---------- 3. 证书 ----------

CERT=""; KEY=""
if [ "$PROXY" = "nginx" ]; then
    LE="/etc/letsencrypt/live/$DOMAIN"
    if [ -f "$LE/fullchain.pem" ]; then
        CERT="$LE/fullchain.pem"; KEY="$LE/privkey.pem"
        ok "复用已有证书：$LE"
    elif command -v certbot >/dev/null 2>&1; then
        info "用 certbot 申请 $DOMAIN 的证书…"
        certbot certonly --nginx -d "$DOMAIN" --non-interactive --agree-tos \
            --register-unsafely-without-email 2>&1 | tail -5 || true
        [ -f "$LE/fullchain.pem" ] || die "证书申请失败。常见原因：DNS 未生效、80 端口不通、云厂商安全组未放行 80/443。"
        CERT="$LE/fullchain.pem"; KEY="$LE/privkey.pem"
        ok "证书已签发"
    else
        die "$DOMAIN 没有现成证书，且未安装 certbot。请先 apt-get install -y certbot python3-certbot-nginx 再重试。"
    fi
else
    ok "Caddy 会自动申请并续期证书，无需手动处理"
fi

# ---------- 4. 写反代配置 ----------

echo
info "写入 $PROXY 配置…"

if [ "$PROXY" = "nginx" ]; then
    CONF="/etc/nginx/conf.d/komari-mcp.conf"
    [ -f "$CONF" ] && { cp "$CONF" "$CONF.bak.$(date +%s)"; warn "已备份原有 $CONF"; }

    # map 只能定义在 http 块内，且全局唯一；已存在就不要重复定义。
    MAP_BLOCK=""
    if ! nginx -T 2>/dev/null | grep -q 'connection_upgrade'; then
        MAP_BLOCK='map $http_upgrade $connection_upgrade {
    default upgrade;
    ""      close;
}
'
    else
        hint "检测到已有 connection_upgrade 映射，不重复定义"
    fi

    # 只有监听标准 443 时，80 跳 443 才有意义；非标准端口下用户必须带端口
    # 访问，做跳转反而会把访客送到一个不存在的地址。
    HTTP_REDIRECT=""
    if [ "$HTTPS_PORT" = "443" ] && [ -z "$(port_owner 80 || true)" -o "$(port_owner 80 || true)" = "nginx" ]; then
        HTTP_REDIRECT='server {
    listen 80;
    server_name '"$DOMAIN"';
    return 301 https://$host$request_uri;
}
'
    fi

    cat > "$CONF" <<NGINX
# 由 setup-mcp-proxy.sh 生成
${MAP_BLOCK}
server {
    listen $HTTPS_PORT ssl;
    http2 on;
    server_name $DOMAIN;

    ssl_certificate     $CERT;
    ssl_certificate_key $KEY;

    # MCP 入口 A：客户端自带 Authorization 头时走这里（更安全，凭据不进 URL）。
    # 必须单独开一个 location —— 下面的 location / 没关缓冲，SSE 推流会被卡住。
    location = /api/mcp {
        proxy_pass http://127.0.0.1:$KOMARI_PORT/api/mcp;
        proxy_http_version 1.1;

        proxy_set_header Host              \$host;
        proxy_set_header X-Forwarded-Proto \$scheme;

        proxy_buffering           off;
        proxy_cache               off;
        chunked_transfer_encoding off;
        proxy_read_timeout        3600s;
    }

    # MCP 入口 B：路径自带密钥，由 nginx 代为注入 Authorization 头。
    # 给填不了请求头的客户端用。
    location = $MCP_PATH {
        proxy_pass http://127.0.0.1:$KOMARI_PORT/api/mcp;
        proxy_http_version 1.1;

        proxy_set_header Authorization     "Bearer $API_KEY";
        proxy_set_header Host              \$host;
        proxy_set_header X-Forwarded-Proto \$scheme;

        # MCP 走 SSE 推流。nginx 默认缓冲响应会把流卡住，必须关掉。
        proxy_buffering           off;
        proxy_cache               off;
        chunked_transfer_encoding off;
        proxy_read_timeout        3600s;
    }

    # 面板、探针上报、网页终端
    location / {
        proxy_pass http://127.0.0.1:$KOMARI_PORT;
        proxy_http_version 1.1;

        # 探针走 WebSocket（/api/clients/v2/rpc），漏了这两行面板能开但探针连不上
        proxy_set_header Upgrade    \$http_upgrade;
        proxy_set_header Connection \$connection_upgrade;

        proxy_set_header Host              \$host;
        proxy_set_header X-Real-IP         \$remote_addr;
        proxy_set_header X-Forwarded-For   \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;

        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}

${HTTP_REDIRECT}
NGINX

    if ! nginx -t 2>&1 | tail -3; then
        rm -f "$CONF"
        die "nginx 配置校验失败，已删除新增的配置文件，现有站点未受影响。"
    fi
    systemctl reload nginx
    ok "nginx 已重载"

else
    CADDYFILE="/etc/caddy/Caddyfile"
    mkdir -p /etc/caddy
    [ -f "$CADDYFILE" ] && { cp "$CADDYFILE" "$CADDYFILE.bak.$(date +%s)"; ok "已备份原有 Caddyfile"; }
    touch "$CADDYFILE"

    # Caddy 用 "域名:端口" 表示非标准端口；标准 443 则直接写域名（它会自动
    # 同时处理 80 跳转与证书申请）。
    if [ "$HTTPS_PORT" = "443" ]; then CADDY_SITE="$DOMAIN"; else CADDY_SITE="$DOMAIN:$HTTPS_PORT"; fi

    if grep -q "^$DOMAIN" "$CADDYFILE" 2>/dev/null; then
        die "Caddyfile 里已存在 $DOMAIN 的站点块，请手动处理后重试，以免覆盖你的配置。"
    fi

    cat >> "$CADDYFILE" <<CADDY

# 由 setup-mcp-proxy.sh 生成
${CADDY_SITE} {
    # MCP 入口 A：客户端自带 Authorization 头时走这里（凭据不进 URL）。
    # Caddy 的 reverse_proxy 默认就支持流式响应，无需额外配置。
    handle /api/mcp* {
        reverse_proxy 127.0.0.1:$KOMARI_PORT
    }

    # MCP 入口 B：路径自带密钥，由 Caddy 代为注入 Authorization 头
    handle $MCP_PATH* {
        rewrite * /api/mcp
        reverse_proxy 127.0.0.1:$KOMARI_PORT {
            header_up Authorization "Bearer $API_KEY"
        }
    }

    # 面板、探针上报、网页终端（reverse_proxy 自带 WebSocket 与流式支持）
    handle {
        reverse_proxy 127.0.0.1:$KOMARI_PORT
    }
}
CADDY

    if ! caddy validate --config "$CADDYFILE" 2>&1 | tail -3; then
        die "Caddyfile 校验失败。原文件已备份为 $CADDYFILE.bak.*，请还原后排查。"
    fi
    systemctl reload caddy
    ok "Caddy 已重载"
fi

# ---------- 5. 把 Komari 锁回本机 ----------

echo
info "把 Komari 限制为只监听 127.0.0.1…"
mkdir -p "$DROPIN_DIR"
cat > "$DROPIN_DIR/override.conf" <<DROPIN
# 由 setup-mcp-proxy.sh 生成。
# 用 drop-in 而非直接改 komari.service：安装器升级时会重新生成那个文件。
[Service]
ExecStart=
ExecStart=$KOMARI_DIR/komari server -l 127.0.0.1:$KOMARI_PORT

# 反代场景必需。MCP SDK 默认启用 DNS rebinding 防护：监听回环地址但
# Host 头不是回环时一律 403 —— 反向代理正好命中这个条件。
Environment=KOMARI_MCP_TRUST_PROXY_HOST=true
Environment=GIN_MODE=release
DROPIN

systemctl daemon-reload
systemctl restart "$SERVICE"
sleep 3
systemctl is-active --quiet "$SERVICE" || die "komari 重启失败，执行 journalctl -u komari -n 50 查看原因。"
ok "komari 已重启，仅监听 127.0.0.1:$KOMARI_PORT"

# ---------- 6. 验证 ----------

echo
info "验证…"
sleep 4
if [ "$HTTPS_PORT" = "443" ]; then BASE="https://$DOMAIN"; else BASE="https://$DOMAIN:$HTTPS_PORT"; fi
URL="$BASE$MCP_PATH"

PANEL_CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 "$BASE/" || echo 000)
case "$PANEL_CODE" in
    200|301|302|307) ok "面板可访问（HTTP $PANEL_CODE）";;
    000) warn "面板无响应。检查 DNS 是否生效、云厂商安全组是否放行 80/443。";;
    *) warn "面板返回 HTTP $PANEL_CODE";;
esac

MCP_BODY=$(curl -s --max-time 20 -X POST "$URL" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"setup-check","version":"1"}}}' 2>/dev/null || true)

MCP_OK=0
case "$MCP_BODY" in
    *'"serverInfo"'*) MCP_OK=1;;
esac

if [ "$MCP_OK" -eq 1 ]; then
    ok "MCP 端点握手成功"
else
    warn "MCP 握手未成功。返回内容："
    echo "${MCP_BODY:0:300}"
    echo
    hint "403  → KOMARI_MCP_TRUST_PROXY_HOST 没生效，检查 systemctl show komari -p Environment"
    hint "401  → API Key 不对，确认后台里已保存"
    hint "超时 → nginx 漏了 proxy_buffering off（SSE 被缓冲卡住）"
fi

# ---------- 7. 输出 ----------

SUMMARY="$KOMARI_DIR/mcp-connector-url.txt"
umask 077
cat > "$SUMMARY" <<TXT
Komari MCP 连接信息（由 setup-mcp-proxy.sh 生成于 $(date -Is)）

面板: $BASE/

接入方式二选一：

【方式 A：自带请求头】推荐。凭据不会出现在 URL 里，也不会进浏览器历史和访问日志。
  URL:    $BASE/api/mcp
  认证:   选择 "No sign-in"（不走 OAuth）
  请求头: Authorization: Bearer $API_KEY

【方式 B：密钥在 URL 里】给填不了请求头的客户端用。
  URL:    $URL
  请求头: 不需要填，$PROXY 会代为注入

两者都等同管理员凭据，请勿公开分享。
TXT

echo
echo "${C_GRN}========================================${C_OFF}"
echo "  配置完成"
echo
echo "  面板:    $BASE/"
echo
echo "  ${C_CYA}方式 A（推荐，凭据不进 URL）${C_OFF}"
echo "    URL:    $BASE/api/mcp"
echo "    认证:   选 \"No sign-in\""
echo "    请求头: Authorization: Bearer <你的 API Key>"
echo
echo "  ${C_CYA}方式 B（客户端填不了请求头时用）${C_OFF}"
echo "    URL:    $URL"
echo "    请求头: 留空，$PROXY 已代为注入"
echo "  已保存到 $SUMMARY（仅 root 可读）"
echo
echo "  ${C_YEL}这个 URL 等同管理员凭据，不要公开分享。${C_OFF}"
echo "${C_GRN}========================================${C_OFF}"
