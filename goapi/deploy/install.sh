#!/usr/bin/env bash
# 风铃分享库 Go 网关 (goapi) 服务器侧安装脚本
#
#   用法:  sudo ./install.sh [--binary /path/to/goapi-linux-amd64] [--nginx-ext-dir DIR]
#          sudo ./install.sh --rollback
#
# 特性: 幂等(可重复执行) / 改动前先备份 / nginx -t 通过才 reload / 一键回滚
# 说明: 本脚本只在服务器上由 root 执行; 它不做任何"业务代码"改动, 只装二进制+env+nginx 片段。
set -euo pipefail

APP_DIR=/opt/fengling
BIN_PATH="$APP_DIR/goapi"
ENV_DIR=/etc/fengling
ENV_FILE="$ENV_DIR/goapi.env"
SERVICE_NAME=fengling-goapi
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
PHP_DIR=/www/wwwroot/flfxk
PHP_CONFIG="$PHP_DIR/config.php"
PHP_API="$PHP_DIR/api.php"
GOAPI_LISTEN=127.0.0.1:9100
BACKUP_ROOT=/root/goapi-backup
NGINX_DIR=/www/server/panel/vhost/nginx

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SNIPPET_SRC="$SCRIPT_DIR/goapi.conf"
SERVICE_SRC="$SCRIPT_DIR/goapi.service"
BIN_SRC="$SCRIPT_DIR/goapi-linux-amd64"
NGINX_EXT_DIR="${NGINX_EXT_DIR:-}"
DO_ROLLBACK=0

while [ $# -gt 0 ]; do
  case "$1" in
    --binary)        BIN_SRC="${2:?}"; shift 2 ;;
    --nginx-ext-dir) NGINX_EXT_DIR="${2:?}"; shift 2 ;;
    --rollback)      DO_ROLLBACK=1; shift ;;
    -h|--help)
      sed -n '2,10p' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

log()  { printf '[goapi-install] %s\n' "$*"; }
die()  { printf '[goapi-install] 错误: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = "0" ] || die "必须以 root 执行"

nginx_reload() {
  nginx -t
  if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet nginx; then
    systemctl reload nginx
  else
    /etc/init.d/nginx reload 2>/dev/null || nginx -s reload
  fi
}

# ---------- 定位 nginx 扩展目录 ----------
detect_ext_dir() {
  [ -n "$NGINX_EXT_DIR" ] && return 0
  # 宝塔: /www/server/panel/vhost/nginx/<站点>.conf 与其扩展目录同名
  local candidate
  candidate="$(grep -lE "root[[:space:]]+${PHP_DIR}|${PHP_DIR}" "$NGINX_DIR"/*.conf 2>/dev/null | head -1 || true)"
  if [ -n "$candidate" ]; then
    local site; site="$(basename "$candidate" .conf)"
    if [ -d "$NGINX_DIR/extension/$site" ]; then
      NGINX_EXT_DIR="$NGINX_DIR/extension/$site"
      return 0
    fi
  fi
  die "无法自动定位 nginx 扩展目录, 请用 --nginx-ext-dir 指定 (例如 /www/server/panel/vhost/nginx/extension/<站点名>)"
}

backup_once() {
  mkdir -p "$BACKUP_ROOT"
  local stamp; stamp="$(date +%Y%m%d-%H%M%S)"
  local tar="$BACKUP_ROOT/goapi-backup-$stamp.tar.gz"
  local files=()
  [ -f "$ENV_FILE" ] && files+=("$ENV_FILE")
  [ -f "$SERVICE_FILE" ] && files+=("$SERVICE_FILE")
  if [ -n "$NGINX_EXT_DIR" ] && [ -f "$NGINX_EXT_DIR/goapi.conf" ]; then
    files+=("$NGINX_EXT_DIR/goapi.conf")
  fi
  for v in "$NGINX_DIR"/*.conf; do [ -f "$v" ] && files+=("$v"); done
  if [ ${#files[@]} -gt 0 ]; then
    tar -czf "$tar" "${files[@]}" 2>/dev/null || true
    chmod 600 "$tar"
    log "已备份 -> $tar"
  fi
  log "备份目录: $BACKUP_ROOT (最近一次: $tar)"
}

# ---------- 回滚 ----------
rollback() {
  log "开始回滚: 删 nginx 片段 + reload + 停服务(保留二进制与 env)"
  if [ -z "$NGINX_EXT_DIR" ]; then
    # 回滚时允许找不到就跳过
    NGINX_EXT_DIR="$(grep -lE "${PHP_DIR}" "$NGINX_DIR"/*.conf 2>/dev/null | head -1 | xargs -r basename -s .conf || true)"
    [ -n "$NGINX_EXT_DIR" ] && NGINX_EXT_DIR="$NGINX_DIR/extension/$NGINX_EXT_DIR"
  fi
  if [ -n "$NGINX_EXT_DIR" ] && [ -f "$NGINX_EXT_DIR/goapi.conf" ]; then
    rm -f "$NGINX_EXT_DIR/goapi.conf"
    log "已删除 $NGINX_EXT_DIR/goapi.conf"
  fi
  nginx_reload && log "nginx 已 reload: 全站现在回到纯 PHP"
  systemctl stop "$SERVICE_NAME" 2>/dev/null || true
  systemctl disable "$SERVICE_NAME" 2>/dev/null || true
  log "服务已停止并禁用(二进制 $BIN_PATH 与 $ENV_FILE 保留)"
  log "验证: curl -sS -o /dev/null -w '%{http_code}\\n' http://127.0.0.1/api.php?action=version"
  exit 0
}

# ---------- 生成 env 文件 ----------
php_const() { # php_const <file> <CONST_NAME>
  grep -E "define\('$2'" "$1" 2>/dev/null | head -1 | sed -E "s/.*define\('$2',[[:space:]]*'([^']*)'.*/\1/"
}

env_quote() { printf '"%s"' "$(printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')"; }

mysql_socket() {
  local s
  s="$(grep -hoE '^[[:space:]]*socket[[:space:]]*=[[:space:]]*[^[:space:]]+' /etc/my.cnf /etc/mysql/my.cnf /etc/mysql/mysql.conf.d/*.cnf 2>/dev/null | head -1 | sed -E 's/.*=[[:space:]]*//' || true)"
  printf '%s' "${s:-/tmp/mysql.sock}"
}

generate_env() {
  [ -f "$PHP_CONFIG" ] || die "找不到 $PHP_CONFIG"
  mkdir -p "$ENV_DIR"
  chmod 700 "$ENV_DIR"
  local tmp; tmp="$(mktemp)"
  umask 077
  {
    echo "# 由 goapi/deploy/install.sh 生成, 请勿手工提交到仓库 (600 权限)"
    echo "GOAPI_ADDR=$(env_quote "$GOAPI_LISTEN")"
    echo "LOG_LEVEL=info"
    echo "FPM_NETWORK=unix"
    echo "FPM_ADDR=$(env_quote /tmp/php-cgi-85.sock)"
    echo "PHP_SCRIPT=$(env_quote "$PHP_API")"
    echo "PHP_DOCROOT=$(env_quote "$PHP_DIR")"
    echo "PHP_SCRIPT_NAME=$(env_quote /api.php)"
    echo "DB_SOCKET=$(env_quote "$(mysql_socket)")"
    echo "DB_HOST=$(env_quote "$(php_const "$PHP_CONFIG" DB_HOST)")"
    echo "DB_PORT=$(env_quote "$(php_const "$PHP_CONFIG" DB_PORT)")"
    echo "DB_NAME=$(env_quote "$(php_const "$PHP_CONFIG" DB_NAME)")"
    echo "DB_USER=$(env_quote "$(php_const "$PHP_CONFIG" DB_USER)")"
    echo "DB_PASS=$(env_quote "$(php_const "$PHP_CONFIG" DB_PASS)")"
    echo "DB_CHARSET=$(env_quote "$(php_const "$PHP_CONFIG" DB_CHARSET)")"
    echo "REDIS_ADDR=$(env_quote 127.0.0.1:6379)"
    echo "REDIS_PASSWORD=$(env_quote '')"
    echo "REDIS_DB=0"
    # 验证码邮件: 与 PHP 的 Mailer 读同一处(config.php 的 SMTP_* 常量)。
    # config.php 没定义时留空 -> goapi 与 PHP 一样直接判定「发送失败」, 不会误发信。
    echo "SMTP_HOST=$(env_quote "$(php_const "$PHP_CONFIG" SMTP_HOST)")"
    echo "SMTP_PORT=$(env_quote "$(php_const "$PHP_CONFIG" SMTP_PORT)")"
    echo "SMTP_USER=$(env_quote "$(php_const "$PHP_CONFIG" SMTP_USER)")"
    echo "SMTP_PASS=$(env_quote "$(php_const "$PHP_CONFIG" SMTP_PASS)")"
    echo "SMTP_FROM_NAME=$(env_quote "$(php_const "$PHP_CONFIG" SMTP_FROM_NAME)")"
    # S3 常量从 api.php 抓(阶段 1+ 会用; 现阶段只是搬过去, 不打印)
    grep -hoE "define\('S3_[A-Z_]+',[[:space:]]*'[^']*'" "$PHP_API" 2>/dev/null \
      | sed -E "s/define\('([A-Z_]+)',[[:space:]]*'([^']*)'/\\1=\"\\2\"/" || true
  } > "$tmp"
  install -m 600 -o root -g root "$tmp" "$ENV_FILE"
  rm -f "$tmp"
  log "已生成 $ENV_FILE (600, 共 $(wc -l < "$ENV_FILE") 行)"
}

# ---------- 安装 ----------
install_all() {
  [ -f "$BIN_SRC" ]        || die "找不到二进制 $BIN_SRC (用 --binary 指定, 或先跑 CI 产物 goapi-linux-amd64)"
  [ -f "$SNIPPET_SRC" ]    || die "找不到 nginx 片段 $SNIPPET_SRC"
  [ -f "$SERVICE_SRC" ]    || die "找不到 systemd 单元 $SERVICE_SRC"
  detect_ext_dir
  log "nginx 扩展目录: $NGINX_EXT_DIR"
  backup_once

  # 1) 二进制
  mkdir -p "$APP_DIR"
  install -m 755 -o root -g root "$BIN_SRC" "$BIN_PATH"
  log "已安装二进制 -> $BIN_PATH"

  # 2) env
  generate_env

  # 3) systemd
  install -m 644 -o root -g root "$SERVICE_SRC" "$SERVICE_FILE"
  systemctl daemon-reload
  systemctl enable "$SERVICE_NAME" >/dev/null
  systemctl restart "$SERVICE_NAME"
  log "服务已启动: systemctl status $SERVICE_NAME"

  # 4) nginx 片段(带守卫; 失败自动还原)
  mkdir -p "$NGINX_EXT_DIR"
  local dst="$NGINX_EXT_DIR/goapi.conf"
  local had=0
  [ -f "$dst" ] && had=1 && cp -a "$dst" "$dst.prev"
  install -m 644 "$SNIPPET_SRC" "$dst"
  if nginx -t; then
    if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet nginx; then
      systemctl reload nginx
    else
      /etc/init.d/nginx reload 2>/dev/null || nginx -s reload
    fi
    rm -f "$dst.prev"
    log "nginx 片段已生效: /api.php 与 /ws -> 127.0.0.1:9100"
  else
    if [ "$had" = 1 ]; then mv -f "$dst.prev" "$dst"; else rm -f "$dst"; fi
    die "nginx -t 失败, 已还原 $dst (请检查主 vhost 是否已有 location = /api.php)"
  fi

  # 5) 健康检查
  sleep 1
  log "健康检查:"
  curl -sS "http://${GOAPI_LISTEN}/api.php?action=health" || true
  echo
  log "回滚命令: $0 --rollback"
}

if [ "$DO_ROLLBACK" = 1 ]; then rollback; else install_all; fi
