#!/usr/bin/env bash
# =============================================================================
# patch-rclone-crypt-password.sh
# -----------------------------------------------------------------------------
# 用途: 一键修复 NAS Backup 服务中 rclone crypt 远端缺失的 password / password2
#       错误信息: "password not set in config file"
#
# 密码派生规则与 Go 代码 (internal/storage.EnsureRcloneConfig) 完全一致:
#     password  = rclone obscure(oss.access_key_secret)
#     password2 = rclone obscure(oss.access_key_secret + "-content-key")
#
# 部署位置: /opt/nasbkup
# 默认路径可通过环境变量或命令行参数覆盖。
#
# 用法:
#   ./patch-rclone-crypt-password.sh
#   ./patch-rclone-crypt-password.sh --app-dir /opt/nasbkup
#   APP_DIR=/data/nasbkup ./patch-rclone-crypt-password.sh
# =============================================================================
set -euo pipefail

# ---------------------------------------------------------------------------
# 路径(可被环境变量 / 命令行参数覆盖)
# ---------------------------------------------------------------------------
APP_DIR="${APP_DIR:-/opt/nasbkup/nas-backup-backend}"
CONFIG_FILE="${CONFIG_FILE:-${APP_DIR}/config.yaml}"
RCLONE_CONF="${RCLONE_CONF:-${APP_DIR}/data/rclone.conf}"
RCLONE_BIN="${RCLONE_BIN:-$(command -v rclone 2>/dev/null || echo rclone)}"

# ---------------------------------------------------------------------------
# 颜色输出
# ---------------------------------------------------------------------------
RED=$'\033[0;31m'; GRN=$'\033[0;32m'; YLW=$'\033[1;33m'; NC=$'\033[0m'
log()  { printf "%s[INFO]%s  %s\n"  "$GRN" "$NC" "$*"; }
warn() { printf "%s[WARN]%s  %s\n"  "$YLW" "$NC" "$*"; }
die()  { printf "%s[ERROR]%s %s\n" "$RED" "$NC" "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# 帮助
# ---------------------------------------------------------------------------
usage() {
    cat <<'USG'
用法: patch-rclone-crypt-password.sh [选项]

选项:
  --app-dir DIR        应用部署根目录 (默认: /opt/nasbkup)
  --config FILE        config.yaml 路径
  --rclone-conf FILE   rclone.conf 路径
  --rclone-bin PATH    rclone 可执行文件路径
  --skip-verify        跳过 rclone lsd 验证(网络不可达时使用)
  -h, --help           显示此帮助
USG
    exit 0
}

SKIP_VERIFY=0
while [ $# -gt 0 ]; do
    case "$1" in
        --app-dir)      APP_DIR="$2";     shift 2 ;;
        --config)       CONFIG_FILE="$2"; shift 2 ;;
        --rclone-conf)  RCLONE_CONF="$2"; shift 2 ;;
        --rclone-bin)   RCLONE_BIN="$2";  shift 2 ;;
        --skip-verify)  SKIP_VERIFY=1;    shift ;;
        -h|--help)      usage ;;
        *)              die "未知参数: $1 (使用 -h 查看帮助)" ;;
    esac
done

# ---------------------------------------------------------------------------
# 前置检查
# ---------------------------------------------------------------------------
[ -d "$APP_DIR" ]   || die "应用目录不存在: $APP_DIR"
[ -f "$CONFIG_FILE" ] || die "config.yaml 不存在: $CONFIG_FILE"
[ -f "$RCLONE_CONF" ] || die "rclone.conf 不存在: $RCLONE_CONF"
command -v "$RCLONE_BIN" >/dev/null 2>&1 \
    || [ -x "$RCLONE_BIN" ] \
    || die "rclone 不可用: $RCLONE_BIN (请先安装 rclone 或通过 --rclone-bin 指定路径)"
command -v python3 >/dev/null 2>&1 || die "缺少 python3,无法解析 YAML"

log "应用目录:    $APP_DIR"
log "配置文件:    $CONFIG_FILE"
log "rclone 配置: $RCLONE_CONF"
log "rclone:      $($RCLONE_BIN version 2>/dev/null | head -n1)"

# ---------------------------------------------------------------------------
# 1) 从 config.yaml 提取 OSS bucket / access_key_secret
# ---------------------------------------------------------------------------
log "解析 OSS 凭证与 bucket ..."
OSS_SK="$(CONFIG_FILE="$CONFIG_FILE" python3 <<'PY'
import os, re, sys
text = open(os.environ["CONFIG_FILE"]).read()

def pick(name):
    # 用更简单的引号处理:不依赖 [\x27] 等转义
    pat = re.compile(rf"^\s*{name}\s*:\s*(.+?)\s*$", re.M)
    for line in text.splitlines():
        m = pat.match(line)
        if not m:
            continue
        v = m.group(1)
        # 去掉首尾引号(单引号或双引号)
        v = v.strip()
        if len(v) >= 2 and v[0] in ("\"", "'") and v[-1] == v[0]:
            v = v[1:-1]
        # 去掉行尾注释
        if "#" in v:
            v = v.split("#", 1)[0].strip()
        return v
    return ""

bucket = pick("bucket")
ak     = pick("access_key_id")
sk     = pick("access_key_secret")
if not sk:
    sys.stderr.write("ERROR: access_key_secret 在 config.yaml 中缺失或为空\n")
    sys.exit(1)
print(sk)
PY
)"

OSS_BUCKET="$(CONFIG_FILE="$CONFIG_FILE" python3 <<'PY'
import os, re
text = open(os.environ["CONFIG_FILE"]).read()
for line in text.splitlines():
    m = re.match(r"^\s*bucket\s*:\s*(.+?)\s*$", line)
    if m:
        v = m.group(1).strip()
        if len(v) >= 2 and v[0] in ("\"", "'") and v[-1] == v[0]:
            v = v[1:-1]
        if "#" in v:
            v = v.split("#", 1)[0].strip()
        print(v)
        break
PY
)"

OSS_AK="$(CONFIG_FILE="$CONFIG_FILE" python3 <<'PY'
import os, re
text = open(os.environ["CONFIG_FILE"]).read()
for line in text.splitlines():
    m = re.match(r"^\s*access_key_id\s*:\s*(.+?)\s*$", line)
    if m:
        v = m.group(1).strip()
        if len(v) >= 2 and v[0] in ("\"", "'") and v[-1] == v[0]:
            v = v[1:-1]
        if "#" in v:
            v = v.split("#", 1)[0].strip()
        print(v)
        break
PY
)"

[ -n "${OSS_SK:-}" ] || die "access_key_secret 为空"
[ -n "${OSS_BUCKET:-}" ] || warn "bucket 为空,生成的 remote 行将缺少 bucket 名称"
log "OSS bucket: ${OSS_BUCKET:-<empty>}"
log "OSS SK 长度: ${#OSS_SK}"

# ---------------------------------------------------------------------------
# 2) 备份现有 rclone.conf
# ---------------------------------------------------------------------------
TS=$(date +%Y%m%d%H%M%S)
BACKUP="${RCLONE_CONF}.bak.${TS}"
cp -p "$RCLONE_CONF" "$BACKUP"
chmod 600 "$BACKUP"
log "已备份: $BACKUP"

# ---------------------------------------------------------------------------
# 3) 用 rclone obscure 派生密码(与 Go 端保持一致)
# ---------------------------------------------------------------------------
log "生成 obscured password / password2 ..."
PASSWORD1=$("$RCLONE_BIN" obscure "$OSS_SK" 2>/dev/null) \
    || die "rclone obscure 失败(密码 1)"
PASSWORD2=$("$RCLONE_BIN" obscure "${OSS_SK}-content-key" 2>/dev/null) \
    || die "rclone obscure 失败(密码 2)"
[ -n "$PASSWORD1" ] && [ -n "$PASSWORD2" ] || die "obscure 输出为空"
log "password  长度: ${#PASSWORD1}"
log "password2 长度: ${#PASSWORD2}"

# ---------------------------------------------------------------------------
# 4) 修补 [oss-crypt] 段:按固定顺序重写整个段,确保 password / password2 存在
# ---------------------------------------------------------------------------
log "修补 $RCLONE_CONF 的 [oss-crypt] 段 ..."
OSS_BUCKET="$OSS_BUCKET" \
PASSWORD1="$PASSWORD1" \
PASSWORD2="$PASSWORD2" \
RCLONE_CONF="$RCLONE_CONF" \
python3 - <<'PY' || { warn "修补失败,原文件未改动,备份在: ${BACKUP}"; exit 1; }
import os, re, sys

path   = os.environ["RCLONE_CONF"]
p1     = os.environ["PASSWORD1"]
p2     = os.environ["PASSWORD2"]
bucket = os.environ.get("OSS_BUCKET", "")

with open(path, "r", encoding="utf-8") as f:
    orig = f.read()

# 找到 [oss-crypt] 段(到下一个 [section] 或文件末尾)
m = re.search(r'^\[oss-crypt\](.*?)(?=^\[|\Z)', orig, flags=re.M | re.S)
if not m:
    sys.stderr.write("ERROR: [oss-crypt] 段未找到,无法修补\n")
    sys.exit(1)

block = m.group(0)

# 解析原段中已存在的字段,缺失时使用默认
def field(text, key, default=""):
    pat = re.compile(rf'^{re.escape(key)}\s*=\s*(.*?)\s*$', re.M)
    hit = pat.search(text)
    return hit.group(1) if hit else default

# 段尾(下一个 [section] 之前的所有内容,以及该 [section] 行本身)
suffix_match = re.search(r'(^\[oss-crypt\].*?)(^\[|\Z)', orig, flags=re.M | re.S)
suffix_start = suffix_match.start(2)  # 下一个 [section] 行或文件末尾的位置

# 构造新的 [oss-crypt] 段(按固定顺序)
new_block_lines = [
    "[oss-crypt]",
    "type = crypt",
]
if bucket:
    new_block_lines.append(f"remote = oss:{bucket}")
new_block_lines.extend([
    "filename_encryption = off",
    "directory_name_encryption = false",
    f"password = {p1}",
    f"password2 = {p2}",
    "",  # 空行,与其它段保持一致
])
new_block = "\n".join(new_block_lines)

# 替换:保留 [oss-crypt] 之前的所有内容 + 新的 [oss-crypt] 段 + 之后的所有内容
new_text = orig[:m.start()] + new_block + orig[suffix_start:]

# 保留原文件权限
import stat
st = os.stat(path)
with open(path, "w", encoding="utf-8") as f:
    f.write(new_text)
os.chmod(path, st.st_mode)
print("OK")
PY

# ---------------------------------------------------------------------------
# 5) 验证
# ---------------------------------------------------------------------------
log "配置语法检查 ..."
if "$RCLONE_BIN" config show --config "$RCLONE_CONF" >/dev/null 2>&1; then
    log "✓ rclone config show 解析通过"
else
    warn "rclone config show 返回非零,配置可能仍有问题"
    "$RCLONE_BIN" config show --config "$RCLONE_CONF" || true
fi

if [ "$SKIP_VERIFY" -eq 0 ]; then
    log "尝试 lsd oss-crypt: (需要访问 OSS) ..."
    if "$RCLONE_BIN" lsd oss-crypt: --config "$RCLONE_CONF" >/dev/null 2>&1; then
        log "✓ oss-crypt remote 可用"
    else
        warn "lsd 失败(可能网络/凭证问题),但密码段已写入。"
        warn "可加 --skip-verify 跳过此步。"
    fi
fi

# ---------------------------------------------------------------------------
# 6) 摘要
# ---------------------------------------------------------------------------
log "修补后的 [oss-crypt] 段(密码已遮蔽):"
# 提取 oss-crypt 段并遮蔽密码(包含 base64 末尾的 =)
python3 - "$RCLONE_CONF" <<'PY'
import re, sys
text = open(sys.argv[1]).read()
m = re.search(r'^\[oss-crypt\](.*?)(?=^\[|\Z)', text, flags=re.M | re.S)
if not m:
    print("    (未找到 [oss-crypt] 段)")
    sys.exit(0)
for line in m.group(1).splitlines():
    line = re.sub(r'^(password2? = )[A-Za-z0-9_=/+]+',
                  r'\1****(obscured)****', line)
    if re.match(r'^\s*(type|remote|filename_encryption|directory_name_encryption|password|password2)\s*=', line):
        print("    " + line)
PY

printf '%s\n' ""
printf '%s\n' "================ 修复完成 ================"
printf '  %s\n' "配置:   $RCLONE_CONF"
printf '  %s\n' "备份:   $BACKUP"
printf '%s\n' ""
printf '  %s\n' "下一步:"
printf '    %s\n' "# 重启服务以应用新配置"
printf '    %s\n' "systemctl restart nas-backup"
printf '    %s\n' "# 或重新部署最新二进制"
log "完成。"
