#!/usr/bin/env bash
# Update an existing native installation from the latest stable release.
# The installer lives in the repository, so fixing a script never requires a
# new release or a rebuild: this wrapper always fetches the current main copy.
set +x
set -Eeuo pipefail
umask 077
export LC_ALL=C

REPO=Flyinsky2004/Email-Invoice-Guard
REF=main
SCRIPT_URL=
CONFIG_DIR=/etc/email-invoice-guard
SERVICE=email-invoice-guard
TMP_DIR=
ARGS=()

usage() {
  cat <<'HELP'
用法：sudo bash update.sh [参数]

从最新稳定 Release 更新已安装的程序，保留配置、SQLite 数据与加密密钥。
安装脚本始终取自仓库 main 分支，脚本修复无需重新发布 Release。

  --repo OWNER/REPO       脚本与 Release 所在仓库，默认 Flyinsky2004/Email-Invoice-Guard
  --ref REF               脚本所在分支或 tag，默认 main
  --script-url URL        直接指定 install.sh 地址（优先级最高）
  --config-dir DIR        已有配置目录，默认 /etc/email-invoice-guard
  --service-name NAME     已有服务名称，默认 email-invoice-guard
  -h, --help              帮助

其余参数原样传给 install.sh，例如 --version v1.0.0、--port 9090。
HELP
}
log() { printf '%s\n' "$*" >&2; }
die() { log "错误：$*"; exit 1; }
need_value() { [[ $# -ge 2 && -n $2 ]] || die "参数 $1 缺少值"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) need_value "$@"; REPO=$2; ARGS+=("$1" "$2"); shift 2 ;;
    --ref) need_value "$@"; REF=$2; shift 2 ;;
    --script-url) need_value "$@"; SCRIPT_URL=$2; shift 2 ;;
    --config-dir) need_value "$@"; CONFIG_DIR=$2; ARGS+=("$1" "$2"); shift 2 ;;
    --service-name) need_value "$@"; SERVICE=${2%.service}; ARGS+=("$1" "$2"); shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) ARGS+=("$1"); shift ;;
  esac
done

[[ $(uname -s) == Linux ]] || die '仅支持 Linux + systemd'
[[ $EUID -eq 0 ]] || die '请使用 sudo 或 root 执行'
[[ -d /run/systemd/system ]] || die '需要正在运行的 systemd'
[[ $REPO =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die '--repo 必须是 OWNER/REPO'
[[ $REF =~ ^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$ ]] || die '--ref 包含不支持的字符'
[[ $CONFIG_DIR =~ ^/[A-Za-z0-9_./-]+$ && $CONFIG_DIR != / && $CONFIG_DIR != *..* && $CONFIG_DIR != */ ]] || die '--config-dir 必须是无空格的绝对路径'
[[ $SERVICE =~ ^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$ ]] || die '服务名称格式错误'

if [[ ! -f $CONFIG_DIR/env ]] && ! systemctl cat "$SERVICE.service" >/dev/null 2>&1; then
  die "未发现已有安装（$CONFIG_DIR/env 或 $SERVICE.service）；首次安装请运行 install.sh"
fi

cleanup() {
  local rc=$?
  trap - EXIT
  [[ -z $TMP_DIR ]] || rm -rf -- "$TMP_DIR"
  exit "$rc"
}
trap cleanup EXIT
fetch() { curl --fail --location --silent --show-error --retry 3 --connect-timeout 15 --max-time 60 --proto '=https' --proto-redir '=https' "$1" -o "$2"; }

# Prefer the installer shipped next to this file (release tarball / git clone);
# fall back to the repository copy so a piped update.sh always gets the newest
# script without a release.
script=
if [[ -n ${BASH_SOURCE[0]:-} && -f ${BASH_SOURCE[0]} ]]; then
  here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
  if [[ -f $here/install.sh ]]; then script=$here/install.sh; fi
fi
if [[ -z $script ]]; then
  command -v curl >/dev/null || die '缺少依赖：curl'
  SCRIPT_URL=${SCRIPT_URL:-https://raw.githubusercontent.com/$REPO/$REF/scripts/install.sh}
  [[ $SCRIPT_URL =~ ^https:// ]] || die '--script-url 必须是 HTTPS 地址'
  TMP_DIR=$(mktemp -d /tmp/email-invoice-guard-update.XXXXXXXX)
  log "获取安装脚本：$SCRIPT_URL"
  fetch "$SCRIPT_URL" "$TMP_DIR/install.sh" || die '无法下载 install.sh'
  [[ $(head -n 1 "$TMP_DIR/install.sh") == '#!/usr/bin/env bash' ]] || die '下载内容不是 install.sh'
  script=$TMP_DIR/install.sh
fi
log "更新：$REPO（脚本：$script）"
bash "$script" "${ARGS[@]}"
