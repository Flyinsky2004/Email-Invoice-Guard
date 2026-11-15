#!/usr/bin/env bash
# Download release binaries; never build on the target machine.
set +x
set -Eeuo pipefail
umask 077
export LC_ALL=C

REPO=Flyinsky2004/Email-Invoice-Guard
VERSION=latest
ARCH=
INSTALL_DIR=/usr/local/lib/email-invoice-guard
CONFIG_DIR=/etc/email-invoice-guard
DATA_DIR=/var/lib/email-invoice-guard
SERVICE=email-invoice-guard
RUN_USER=email-invoice-guard
BOT_TOKEN=${TELEGRAM_BOT_TOKEN:-}
USER_IDS=${TELEGRAM_ALLOWED_USER_IDS:-}
CHAT_ID=${TELEGRAM_CHAT_ID:-}
PUBLIC_URL=${PUBLIC_BASE_URL:-}
LISTEN=127.0.0.1:8080
ZONE=Asia/Taipei
REMINDER=09:00
NON_INTERACTIVE=0
RECONFIGURE=0
NO_START=0
SKIP_DEPS=0
TOKEN_FILE=
LISTEN_SET=0
ZONE_SET=0
REMINDER_SET=0
DATA_SET=0
DB_PATH=
HEALTH_PATH=/healthz
SYNC_INTERVAL=60s
SHUTDOWN_TIMEOUT=15s
PROXY_CIDRS=127.0.0.1/32,::1/128
TMP_DIR=
CHANGED=0
OLD_BINARY=0
OLD_ENV=0
OLD_UNIT=0
WAS_ACTIVE=0
WAS_ENABLED=0

usage() {
  cat <<'HELP'
用法：sudo bash install.sh [参数]

默认从 Flyinsky2004/Email-Invoice-Guard 下载最新稳定 Release，校验 SHA-256，
安装并启动 systemd 服务。首次安装会隐藏询问 Bot Token，并询问信任用户 ID。

  --version TAG           指定 Release tag，默认 latest（不选择预发布）
  --repo OWNER/REPO       GitHub 仓库
  --arch amd64|arm64      默认自动检测
  --bot-token TOKEN       Bot Token（建议交互输入或用 --bot-token-file）
  --bot-token-file FILE   从文件读取 Bot Token，避免写入命令历史
  --user-ids IDS          逗号分隔的 Telegram 信任用户 ID
  --chat-id ID            目标会话，默认第一个信任用户 ID；支持负数群组 ID
  --public-url URL        公网 HTTPS 源站；未设置则询问，可留空使用本机模式
  --listen HOST:PORT      默认 127.0.0.1:8080
  --timezone ZONE         默认 Asia/Taipei
  --reminder-time HH:MM   默认 09:00
  --install-dir DIR      二进制目录
  --config-dir DIR       配置与密钥目录
  --data-dir DIR         SQLite 数据目录
  --service-name NAME    systemd 服务名称（不含 .service）
  --reconfigure          修改已有配置；保留未知配置项与原加密密钥
  --non-interactive      不询问，缺少必填项时失败
  --skip-deps            不通过 apt 安装依赖
  --no-start             仅安装，不启用或启动服务
  -h, --help             帮助

重复运行默认只升级程序，保留配置、数据库及加密密钥。
公网邮箱授权需要 HTTPS 反向代理；本机模式只先启动 Bot 与本机管理端口。
HELP
}
log() { printf '%s\n' "$*" >&2; }
die() { log "错误：$*"; exit 1; }
need_value() { [[ $# -ge 2 && -n $2 ]] || die "参数 $1 缺少值"; }
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) need_value "$@"; VERSION=$2; shift 2 ;;
    --repo) need_value "$@"; REPO=$2; shift 2 ;;
    --arch) need_value "$@"; ARCH=$2; shift 2 ;;
    --bot-token) need_value "$@"; BOT_TOKEN=$2; RECONFIGURE=1; shift 2 ;;
    --bot-token-file) need_value "$@"; TOKEN_FILE=$2; RECONFIGURE=1; shift 2 ;;
    --user-ids) need_value "$@"; USER_IDS=$2; RECONFIGURE=1; shift 2 ;;
    --chat-id) need_value "$@"; CHAT_ID=$2; RECONFIGURE=1; shift 2 ;;
    --public-url) need_value "$@"; PUBLIC_URL=$2; RECONFIGURE=1; shift 2 ;;
    --listen) need_value "$@"; LISTEN=$2; LISTEN_SET=1; RECONFIGURE=1; shift 2 ;;
    --timezone) need_value "$@"; ZONE=$2; ZONE_SET=1; RECONFIGURE=1; shift 2 ;;
    --reminder-time) need_value "$@"; REMINDER=$2; REMINDER_SET=1; RECONFIGURE=1; shift 2 ;;
    --install-dir) need_value "$@"; INSTALL_DIR=$2; shift 2 ;;
    --config-dir) need_value "$@"; CONFIG_DIR=$2; shift 2 ;;
    --data-dir) need_value "$@"; DATA_DIR=$2; DATA_SET=1; RECONFIGURE=1; shift 2 ;;
    --service-name) need_value "$@"; SERVICE=${2%.service}; shift 2 ;;
    --reconfigure) RECONFIGURE=1; shift ;;
    --non-interactive|--yes) NON_INTERACTIVE=1; shift ;;
    --skip-deps) SKIP_DEPS=1; shift ;;
    --no-start) NO_START=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1（使用 --help）" ;;
  esac
done

[[ $(uname -s) == Linux ]] || die '仅支持 Linux + systemd'
[[ $EUID -eq 0 ]] || die '请使用 sudo 或 root 执行'
[[ -d /run/systemd/system ]] || die '需要正在运行的 systemd'
[[ $REPO =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die '--repo 必须是 OWNER/REPO'
[[ $VERSION =~ ^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$ ]] || die 'Release tag 包含不支持的字符'
[[ $SERVICE =~ ^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$ ]] || die '服务名称格式错误'
for path in "$INSTALL_DIR" "$CONFIG_DIR" "$DATA_DIR"; do
  case "$path" in /home|/home/*|/root|/root/*|/run/user|/run/user/*) die '自定义目录不能位于被 ProtectHome 屏蔽的位置' ;; esac
  [[ $path =~ ^/[A-Za-z0-9_./-]+$ && $path != / && $path != *..* && $path != */ ]] || die '目录必须是无空格、无 .. 的绝对路径，且不以 / 结尾'
done
[[ $INSTALL_DIR != "$CONFIG_DIR" && $DATA_DIR != "$CONFIG_DIR" && $DATA_DIR != "$INSTALL_DIR" ]] || die '程序、配置、数据目录必须不同'
BIN=$INSTALL_DIR/email-invoice-guard
ENV_FILE=$CONFIG_DIR/env
KEY_FILE=$CONFIG_DIR/encryption.key
UNIT=/etc/systemd/system/$SERVICE.service
for path in "$INSTALL_DIR" "$CONFIG_DIR" "$DATA_DIR" "$BIN" "$ENV_FILE" "$KEY_FILE" "$UNIT"; do
  [[ ! -L $path ]] || die '安装目标不能是符号链接'
done
command -v flock >/dev/null || die '缺少 flock，请安装 util-linux'
exec 9>/run/lock/email-invoice-guard-install.lock
flock -n 9 || die '已有安装任务运行'

# Read only our EnvironmentFile syntax; never execute/source a configuration file.
env_value() {
  local key=$1 line value
  [[ -f $ENV_FILE ]] || return 0
  while IFS= read -r line || [[ -n $line ]]; do
    if [[ $line == "$key="* ]]; then
      value=${line#*=}
      if [[ $value == \"*\" ]]; then
        value=${value:1:${#value}-2}
        value=${value//\\\"/\"}
        value=${value//\\\\/\\}
      fi
      printf '%s' "$value"
      return 0
    fi
  done < "$ENV_FILE"
}
[[ ! -f $ENV_FILE ]] || OLD_ENV=1
if [[ $OLD_ENV == 1 ]]; then
  [[ -n $BOT_TOKEN ]] || BOT_TOKEN=$(env_value TELEGRAM_BOT_TOKEN)
  [[ -n $USER_IDS ]] || USER_IDS=$(env_value TELEGRAM_ALLOWED_USER_IDS)
  [[ -n $CHAT_ID ]] || CHAT_ID=$(env_value TELEGRAM_CHAT_ID)
  [[ -n $PUBLIC_URL ]] || PUBLIC_URL=$(env_value PUBLIC_BASE_URL)
  if [[ $LISTEN_SET == 0 ]]; then prior=$(env_value LISTEN_ADDRESS); LISTEN=${prior:-$LISTEN}; fi
  if [[ $ZONE_SET == 0 ]]; then prior=$(env_value TIMEZONE); ZONE=${prior:-$ZONE}; fi
  if [[ $REMINDER_SET == 0 ]]; then prior=$(env_value REMINDER_TIME); REMINDER=${prior:-$REMINDER}; fi
  prior=$(env_value HEALTH_PATH); HEALTH_PATH=${prior:-$HEALTH_PATH}
  prior=$(env_value SYNC_INTERVAL); SYNC_INTERVAL=${prior:-$SYNC_INTERVAL}
  prior=$(env_value SHUTDOWN_TIMEOUT); SHUTDOWN_TIMEOUT=${prior:-$SHUTDOWN_TIMEOUT}
  prior=$(env_value TRUSTED_PROXY_CIDRS); PROXY_CIDRS=${prior:-$PROXY_CIDRS}
  if [[ $DATA_SET == 0 ]]; then
    DB_PATH=$(env_value DATABASE_PATH)
    if [[ -n $DB_PATH ]]; then DATA_DIR=${DB_PATH%/*}; fi
  fi
fi
DB_PATH=${DB_PATH:-$DATA_DIR/invoice-guard.db}
for path in "$DATA_DIR" "$DB_PATH"; do
  case "$path" in /home|/home/*|/root|/root/*|/run/user|/run/user/*) die '数据路径不能位于被 ProtectHome 屏蔽的位置' ;; esac
  [[ $path =~ ^/[A-Za-z0-9_./-]+$ && $path != / && $path != *..* && $path != */ && ! -L $path ]] || die '已有数据目录/路径格式不支持，请检查配置'
done
if [[ -n $TOKEN_FILE ]]; then
  [[ -f $TOKEN_FILE && -r $TOKEN_FILE ]] || die 'Bot Token 文件不可读'
  BOT_TOKEN=$(cat -- "$TOKEN_FILE")
fi
CAN_PROMPT=0
if [[ $NON_INTERACTIVE == 0 ]] && { exec 3<>/dev/tty; } 2>/dev/null; then CAN_PROMPT=1; fi
prompt() {
  local name=$1 label=$2 secret=${3:-0} value
  [[ $CAN_PROMPT == 1 ]] || die "缺少 $name，请通过参数或环境变量提供"
  printf '%s' "$label" >&3
  if [[ $secret == 1 ]]; then IFS= read -r -s value <&3; printf '\n' >&3; else IFS= read -r value <&3; fi
  printf -v "$name" '%s' "$value"
}
if [[ $OLD_ENV == 0 || $RECONFIGURE == 1 ]]; then
  [[ -n $BOT_TOKEN ]] || prompt BOT_TOKEN 'Telegram Bot Token（隐藏输入）：' 1
  [[ -n $USER_IDS ]] || prompt USER_IDS '信任用户 ID（多个用逗号分隔）：'
  if [[ -z $PUBLIC_URL && $CAN_PROMPT == 1 ]]; then prompt PUBLIC_URL '公网 HTTPS 地址（留空先使用本机模式）：'; fi
fi
[[ $BOT_TOKEN =~ ^[0-9]+:[A-Za-z0-9_-]+$ ]] || die 'Bot Token 格式错误'
[[ $USER_IDS != *$'\n'* && $USER_IDS != *$'\r'* ]] || die '用户 ID 必须在同一行'
USER_IDS=${USER_IDS// /}; USER_IDS=${USER_IDS//$'\t'/}
[[ $USER_IDS =~ ^[1-9][0-9]*(,[1-9][0-9]*)*$ ]] || die '信任用户 ID 必须为逗号分隔的正整数'
valid_id() {
  local number=${1#-}
  [[ $number =~ ^[1-9][0-9]*$ ]] || return 1
  [[ ${#number} -lt 19 || ( ${#number} -eq 19 && ! $number > 9223372036854775807 ) ]]
}
IFS=',' read -r -a trusted_ids <<< "$USER_IDS"
for id in "${trusted_ids[@]}"; do valid_id "$id" || die '用户 ID 超出 int64 范围'; done
CHAT_ID=${CHAT_ID:-${trusted_ids[0]}}
[[ $CHAT_ID =~ ^-?[1-9][0-9]*$ ]] && valid_id "$CHAT_ID" || die 'Chat ID 格式错误'
[[ $LISTEN =~ ^([A-Za-z0-9._-]*|\[[0-9A-Fa-f:]+\]):[0-9]{1,5}$ ]] || die '--listen 必须是 HOST:PORT'
port=${LISTEN##*:}; (( 10#$port > 0 && 10#$port <= 65535 )) || die '监听端口范围错误'
PUBLIC_URL=${PUBLIC_URL%/}
PUBLIC_URL=${PUBLIC_URL:-http://127.0.0.1:$port}
ALLOW_HTTP=false
if [[ $PUBLIC_URL =~ ^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?$ ]]; then
  :
elif [[ $PUBLIC_URL =~ ^http://(127\.0\.0\.1|localhost|\[::1\])(:[0-9]{1,5})?$ ]]; then
  ALLOW_HTTP=true
else die '公网地址必须为 HTTPS 源站（无路径、查询或用户信息）；HTTP 仅允许回环地址'
fi
[[ $ZONE =~ ^[A-Za-z0-9_+/-]+$ && $ZONE != /* && $ZONE != *..* ]] || die '时区格式错误'
[[ $REMINDER =~ ^([01][0-9]|2[0-3]):[0-5][0-9]$ ]] || die '提醒时间必须是 HH:MM'
if [[ -z $ARCH ]]; then
  case $(uname -m) in x86_64|amd64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) die '仅提供 amd64 和 arm64 安装包' ;; esac
fi
[[ $ARCH == amd64 || $ARCH == arm64 ]] || die '--arch 仅支持 amd64 或 arm64'

if [[ $SKIP_DEPS == 0 ]]; then
  command -v apt-get >/dev/null || die '自动安装依赖支持 Debian/Ubuntu；其他系统先安装依赖，再加 --skip-deps'
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq ca-certificates curl tar coreutils openssl jq tzdata passwd
fi
for tool in curl tar sha256sum openssl jq getent useradd groupadd install systemctl runuser; do
  command -v "$tool" >/dev/null || die "缺少依赖：$tool"
done
[[ -f /usr/share/zoneinfo/$ZONE ]] || die '找不到时区文件，请安装 tzdata'
TMP_DIR=$(mktemp -d /tmp/email-invoice-guard.XXXXXXXX)
cleanup() {
  local rc=$?
  trap - EXIT
  set +e
  if [[ $rc != 0 && $CHANGED == 1 ]]; then
    log '安装未完成，恢复原程序、配置和服务文件（不修改数据库或加密密钥）。'
    if [[ $NO_START == 0 ]]; then systemctl stop "$SERVICE.service" >/dev/null 2>&1 || true; fi
    if [[ $OLD_BINARY == 1 ]]; then
      install -m 0755 "$TMP_DIR/old-binary" "$BIN.rollback"
      mv -f -- "$BIN.rollback" "$BIN"
    else rm -f -- "$BIN"; fi
    if [[ $OLD_ENV == 1 ]]; then cp -p -- "$TMP_DIR/old-env" "$ENV_FILE"; else rm -f -- "$ENV_FILE"; fi
    if [[ $OLD_UNIT == 1 ]]; then cp -p -- "$TMP_DIR/old-unit" "$UNIT"; else rm -f -- "$UNIT"; fi
    if [[ $WAS_ENABLED == 0 ]]; then systemctl disable "$SERVICE.service" >/dev/null 2>&1 || true; fi
    systemctl daemon-reload || true
    if [[ $WAS_ACTIVE == 1 && $NO_START == 0 ]]; then systemctl start "$SERVICE.service" || true; fi
  fi
  [[ -z $TMP_DIR ]] || rm -rf -- "$TMP_DIR"
  exit "$rc"
}
trap cleanup EXIT
fetch() { curl --fail --location --silent --show-error --retry 3 --connect-timeout 15 --max-time 180 --proto '=https' --proto-redir '=https' "$1" -o "$2"; }
API=https://api.github.com/repos/$REPO/releases
if [[ $VERSION == latest ]]; then endpoint=$API/latest; else endpoint=$API/tags/$VERSION; fi
log "查询 Release：$REPO ($VERSION)"
fetch "$endpoint" "$TMP_DIR/release.json" || die '无法读取 Release：确认已发布且 Actions 已完成，或稍后重试 GitHub API'
TAG=$(jq -er '.tag_name' "$TMP_DIR/release.json")
[[ $TAG =~ ^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$ ]] || die 'Release tag 格式不支持'
ASSET=email-invoice-guard_${TAG}_linux_${ARCH}.tar.gz
asset_url() {
  local url
  url=$(jq -er --arg name "$1" '.assets[] | select(.name == $name) | .browser_download_url' "$TMP_DIR/release.json") || die "Release 缺少 $1，请等待 Actions 完成"
  [[ $url == "https://github.com/$REPO/releases/download/$TAG/"* ]] || die 'Release 下载地址不匹配仓库'
  printf '%s' "$url"
}
fetch "$(asset_url "$ASSET")" "$TMP_DIR/package.tar.gz"
fetch "$(asset_url SHA256SUMS)" "$TMP_DIR/SHA256SUMS"
expected=$(awk -v name="$ASSET" '$2 == name {print $1}' "$TMP_DIR/SHA256SUMS")
[[ $expected =~ ^[0-9a-fA-F]{64}$ ]] || die 'SHA256SUMS 缺少唯一有效校验值'
actual=$(sha256sum "$TMP_DIR/package.tar.gz"); actual=${actual%% *}
[[ $actual == "$expected" ]] || die '下载包 SHA-256 校验失败'
# Stream only the executable to a private file: never extract archive paths as root.
tar -tzf "$TMP_DIR/package.tar.gz" > "$TMP_DIR/entries"
member=
while IFS= read -r entry; do
  if [[ $entry == email-invoice-guard || $entry == ./email-invoice-guard ]]; then
    [[ -z $member ]] || die '压缩包有重复二进制入口'
    member=$entry
  fi
done < "$TMP_DIR/entries"
[[ -n $member ]] || die '安装包缺少 email-invoice-guard'
tar -xOzf "$TMP_DIR/package.tar.gz" -- "$member" > "$TMP_DIR/binary"
[[ -s $TMP_DIR/binary ]] || die '安装包二进制为空'
chmod 0755 "$TMP_DIR/binary"
installed_version=$("$TMP_DIR/binary" --version) || die '二进制无法运行，请检查 CPU 架构'
[[ $installed_version == "Email-Invoice-Guard $TAG "* ]] || die '二进制版本与 Release 不匹配'

if [[ -f $UNIT ]]; then
  OLD_UNIT=1
  grep -q '^# Managed by Email-Invoice-Guard installer\.$' "$UNIT" || die '目标服务不是本安装器管理，请保留原文件并指定另一个 --service-name'
  cp -p -- "$UNIT" "$TMP_DIR/old-unit"
fi
if [[ -f $BIN ]]; then OLD_BINARY=1; cp -p -- "$BIN" "$TMP_DIR/old-binary"; fi
if [[ $OLD_ENV == 1 ]]; then cp -p -- "$ENV_FILE" "$TMP_DIR/old-env"; fi
if systemctl is-active --quiet "$SERVICE.service"; then WAS_ACTIVE=1; fi
if systemctl is-enabled --quiet "$SERVICE.service"; then WAS_ENABLED=1; fi
if ! getent group "$RUN_USER" >/dev/null; then groupadd --system "$RUN_USER"; fi
if ! id "$RUN_USER" >/dev/null 2>&1; then useradd --system --gid "$RUN_USER" --home-dir "$DATA_DIR" --no-create-home --shell /usr/sbin/nologin "$RUN_USER"; fi
[[ $(id -u "$RUN_USER") != 0 ]] || die '拒绝使用 UID 0 的服务用户'
install -d -m 0755 -o root -g root "$INSTALL_DIR"
install -d -m 0750 -o root -g "$RUN_USER" "$CONFIG_DIR"
if [[ ! -d $DATA_DIR ]]; then install -d -m 0700 -o "$RUN_USER" -g "$RUN_USER" "$DATA_DIR"; fi
runuser -u "$RUN_USER" -- test -w "$DATA_DIR" || die '数据目录不可写，请检查服务用户属主'

if [[ $OLD_ENV == 0 || $RECONFIGURE == 1 ]]; then
  old_key=$(env_value ENCRYPTION_KEY)
  old_key_file=$(env_value ENCRYPTION_KEY_FILE)
  [[ -z $old_key || -z $old_key_file ]] || die '已有配置同时指定了两种加密密钥'
  keys=(PUBLIC_BASE_URL TELEGRAM_BOT_TOKEN TELEGRAM_CHAT_ID TELEGRAM_ALLOWED_USER_IDS DATABASE_PATH LISTEN_ADDRESS HEALTH_PATH TIMEZONE REMINDER_TIME SYNC_INTERVAL SHUTDOWN_TIMEOUT TRUSTED_PROXY_CIDRS ALLOW_INSECURE_HTTP)
  values=("$PUBLIC_URL" "$BOT_TOKEN" "$CHAT_ID" "$USER_IDS" "$DB_PATH" "$LISTEN" "$HEALTH_PATH" "$ZONE" "$REMINDER" "$SYNC_INTERVAL" "$SHUTDOWN_TIMEOUT" "$PROXY_CIDRS" "$ALLOW_HTTP")
  if [[ -z $old_key && -z $old_key_file ]]; then
    if [[ ! -f $KEY_FILE ]]; then openssl rand -base64 32 > "$TMP_DIR/key"; install -m 0400 -o "$RUN_USER" -g "$RUN_USER" "$TMP_DIR/key" "$KEY_FILE"; fi
    runuser -u "$RUN_USER" -- test -r "$KEY_FILE" || die '服务用户无法读取已有密钥，拒绝生成替代密钥'
    bytes=$(base64 --decode < "$KEY_FILE" | wc -c)
    [[ $bytes -eq 32 ]] || die '已有密钥格式无效，拒绝替换'
    keys+=(ENCRYPTION_KEY_FILE); values+=("$KEY_FILE")
  fi
  : > "$TMP_DIR/new-env"
  if [[ $OLD_ENV == 1 ]]; then
    while IFS= read -r line || [[ -n $line ]]; do
      keep=1
      for key in "${keys[@]}"; do if [[ $line == "$key="* ]]; then keep=0; break; fi; done
      if [[ $keep == 1 ]]; then printf '%s\n' "$line" >> "$TMP_DIR/new-env"; fi
    done < "$ENV_FILE"
  else printf '# Managed by Email-Invoice-Guard installer. Keep this file private.\n' > "$TMP_DIR/new-env"; fi
  for ((i=0;i<${#keys[@]};i++)); do
    value=${values[$i]//\\/\\\\}; value=${value//\"/\\\"}
    printf '%s="%s"\n' "${keys[$i]}" "$value" >> "$TMP_DIR/new-env"
  done
fi
cat > "$TMP_DIR/new-unit" <<UNIT
# Managed by Email-Invoice-Guard installer.
[Unit]
Description=Email-Invoice-Guard
Wants=network-online.target
After=network-online.target
StartLimitIntervalSec=60
StartLimitBurst=5
[Service]
Type=simple
User=$RUN_USER
Group=$RUN_USER
EnvironmentFile=$ENV_FILE
WorkingDirectory=$DATA_DIR
ExecStart=$BIN
Restart=on-failure
RestartSec=5s
TimeoutStopSec=30s
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$DATA_DIR
[Install]
WantedBy=multi-user.target
UNIT
CHANGED=1
if [[ $WAS_ACTIVE == 1 && $NO_START == 0 ]]; then systemctl stop "$SERVICE.service"; fi
next_binary=$(mktemp "$INSTALL_DIR/.binary.XXXXXXXX")
install -m 0755 "$TMP_DIR/binary" "$next_binary"
mv -f -- "$next_binary" "$BIN"
if [[ -f $TMP_DIR/new-env ]]; then
  next_env=$(mktemp "$CONFIG_DIR/.env.XXXXXXXX")
  install -m 0600 -o root -g root "$TMP_DIR/new-env" "$next_env"
  mv -f -- "$next_env" "$ENV_FILE"
fi
install -m 0644 "$TMP_DIR/new-unit" "$UNIT"
systemctl daemon-reload
if [[ $NO_START == 0 ]]; then
  systemctl start "$SERVICE.service"
  healthy=0
  for ((i=0;i<20;i++)); do
    if systemctl is-active --quiet "$SERVICE.service" && env LISTEN_ADDRESS="$LISTEN" HEALTH_PATH="$HEALTH_PATH" "$BIN" --healthcheck >/dev/null 2>&1; then sleep 1; if systemctl is-active --quiet "$SERVICE.service"; then healthy=1; break; fi; fi
    sleep 1
  done
  [[ $healthy == 1 ]] || die '服务未启动；可查看 journalctl 日志（原文件将恢复）'
  systemctl enable "$SERVICE.service" >/dev/null
fi
CHANGED=0
log "已安装 $TAG ($ARCH)。配置：$ENV_FILE；数据：$DATA_DIR"
log "管理：systemctl status $SERVICE；日志：journalctl -u $SERVICE -f"
if [[ $ALLOW_HTTP == true && ( $OLD_ENV == 0 || $RECONFIGURE == 1 ) ]]; then log '当前为本机模式；添加邮箱前请配置公网 HTTPS 源站与反向代理。'; fi
