#!/usr/bin/env bash
#
# 影策（open-ai-canvas）本地一键更新脚本（宿主机，非 Docker）
#
#   ./scripts/update-all.sh             拉取官方 upstream/main 并合并进本地 main
#   ./scripts/update-all.sh --check     只检查有没有新代码，不更新
#   ./scripts/update-all.sh --dry-run   预演：做冲突试算，不改动工作区
#   ./scripts/update-all.sh --stash     自动暂存本地未提交改动，更新后恢复
#   ./scripts/update-all.sh --merge     本地提交用 merge（默认，保留两边提交）
#   ./scripts/update-all.sh --rebase    本地提交变基到官方之上（会重写本地提交）
#   ./scripts/update-all.sh --ff-only   只允许快进，本地有提交就停止
#   ./scripts/update-all.sh --push      更新成功后把 main 推送到自己的 origin
#   ./scripts/update-all.sh --deps      更新成功后同步前后端依赖
#   ./scripts/update-all.sh --restart   更新成功后重启本地服务
#   ./scripts/update-all.sh --help
#
# 远端拓扑（本仓库是官方仓库的 fork，两个远端各司其职）：
#   origin   → https://github.com/zjhr/open-ai-canvas.git      你的仓库，只用来推送
#   upstream → https://github.com/ddcat-ai/open-ai-canvas.git  官方仓库，只用来拉取
#   本脚本默认从 upstream 拉取官方代码并合并进本地 main；推送自己的改动用
#   git push origin main（或加 --push 让脚本更新成功后代推）。
#
# 退出码：
#   0  已是最新，或更新成功
#   1  需要 agent 介入（冲突 / 本地改动会被覆盖，工作区已还原）
#   2  用法或环境不满足（不是仓库、分支不对、上一次操作未结束……）
#   3  其它失败（网络、git 命令报错）
#
# 设计要点（本机实际踩过的坑，改动前请先看这里）：
#   1. 本机环境存在损坏的 GIT_CONFIG_COUNT，会让所有 git 命令直接报
#      “缺失配置键名 GIT_CONFIG_KEY_0”；脚本内统一清掉这几个变量。
#   2. 冲突先预检、后落盘：用 git merge-tree 在内存里试算合并结果，
#      有冲突就直接停在工作区原状，绝不把半个合并/变基留给用户。
#   3. 动历史之前先记 refs/canvas-update/backup-* 备份引用；失败只做
#      --abort 还原，不 reset --hard、不 checkout --、不清理工作区。
#   4. 本地未提交改动默认不自动暂存：只有改动文件与上游改动不重叠时才
#      继续更新；重叠（会被覆盖）就直接停下交给 agent，除非显式 --stash。
#      --stash 只收必要的文件（能只收挡路的未跟踪文件就不动其它未跟踪文件），
#      恢复失败时 stash 一定留着，不让改动凭空消失。
#   5. 非 ASCII 文件名必须用 -z 读取，否则 git 输出带引号的八进制转义。
#   6. 上游强推（重写历史）时不自动变基/合并，停下交给 agent 判断。
#   7. 全程 tee 到 .local/logs/update-<时间戳>.log；冲突时把日志路径和
#      冲突清单一起打印，用户直接照着交给 agent。
#   8. 合并官方代码用 merge（默认）：本地提交保留、官方提交并入，历史真实、
#      可回退，也不会重写你自己的提交 SHA。想线性历史才显式加 --rebase。
#   9. 只推送到 origin（自己的 fork）；upstream 的 push URL 已置为只读占位，
#      避免手滑把本地改动直接推到官方仓库。
#
set -Eeuo pipefail

# ---------------------------------------------------------------------------
# 基础路径与参数
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
LOCAL_DIR="$ROOT/.local"
LOG_DIR="$LOCAL_DIR/logs"
TMP_ROOT="$LOCAL_DIR/tmp"

BRANCH="${CANVAS_UPDATE_BRANCH:-main}"
# 默认从官方仓库拉取（本仓库是官方仓库的 fork）；origin 留给自己推送
REMOTE="${CANVAS_UPDATE_REMOTE:-upstream}"
PUSH_REMOTE="${CANVAS_UPDATE_PUSH_REMOTE:-origin}"

MODE="apply"          # apply | check | dry
STRATEGY="merge"      # merge | rebase | ff-only
USE_STASH=0
SYNC_DEPS=0
DO_RESTART=0
DO_PUSH=0

EXIT_OK=0
EXIT_NEED_AGENT=1
EXIT_USAGE=2
EXIT_FAILED=3

# 运行状态
LOG_FILE=""
TMP_DIR=""
LOCAL_SHA=""
REMOTE_SHA=""
PRE_FETCH_REMOTE_SHA=""
CURRENT_BRANCH=""
BEHIND=0
AHEAD=0
BACKUP_REF=""
STASH_SHA=""
STASH_RESTORED=0
UPSTREAM_REWRITTEN=0
DEPS_CHANGED=""
DEPS_FAILED=0
NEW_SHA=""

usage() {
    cat <<'USAGE'
影策（open-ai-canvas）本地一键更新脚本

用法：
  ./scripts/update-all.sh             拉取官方 upstream/main 并合并进本地 main
  ./scripts/update-all.sh --check     只检查有没有新代码，不更新
  ./scripts/update-all.sh --dry-run   预演：做冲突试算，不改动工作区
  ./scripts/update-all.sh --stash     自动暂存本地未提交改动，更新后恢复
  ./scripts/update-all.sh --merge     本地提交用 merge（默认，保留两边提交）
  ./scripts/update-all.sh --rebase    本地提交变基到官方之上（会重写本地提交）
  ./scripts/update-all.sh --ff-only   只允许快进，本地有提交就停止
  ./scripts/update-all.sh --push      更新成功后把 main 推送到自己的 origin
  ./scripts/update-all.sh --deps      更新成功后同步前后端依赖
  ./scripts/update-all.sh --restart   更新成功后重启本地服务
  ./scripts/update-all.sh --help

退出码：
  0 已是最新或更新成功 · 1 需要 agent 介入 · 2 用法/环境问题 · 3 其它失败

远端：
  upstream = 官方仓库（ddcat-ai/open-ai-canvas），只拉取
  origin   = 你自己的仓库（zjhr/open-ai-canvas），只推送
  fork 场景下「更新」= 把官方 upstream/main 合并进本地 main，你的提交会保留。

有冲突时脚本会停在工作区原状，并打印冲突清单和日志路径，照着交给 agent 即可。
可用环境变量覆盖默认远端和分支：CANVAS_UPDATE_REMOTE（默认 upstream）、
CANVAS_UPDATE_PUSH_REMOTE（默认 origin）、CANVAS_UPDATE_BRANCH（默认 main）。
USAGE
}

while (( $# > 0 )); do
    case "$1" in
        --check)    MODE="check" ;;
        --dry-run)  MODE="dry" ;;
        --stash)    USE_STASH=1 ;;
        --merge)    STRATEGY="merge" ;;
        --rebase)   STRATEGY="rebase" ;;
        --ff-only)  STRATEGY="ff-only" ;;
        --push)     DO_PUSH=1 ;;
        --deps)     SYNC_DEPS=1 ;;
        --restart)  DO_RESTART=1 ;;
        -h|--help)  usage; exit "$EXIT_OK" ;;
        *)
            printf '未知参数：%s\n\n' "$1" >&2
            usage >&2
            exit "$EXIT_USAGE"
            ;;
    esac
    shift
done

# 损坏的 GIT 环境变量会让 git / go 直接失败，脚本内一律清掉
unset GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_KEY_1 2>/dev/null || true
unset GIT_CONFIG_VALUE_0 GIT_CONFIG_VALUE_1 2>/dev/null || true
export GIT_TERMINAL_PROMPT=0
export GIT_PAGER=cat

# ---------------------------------------------------------------------------
# 输出
# ---------------------------------------------------------------------------
IS_TTY=0
[[ -t 1 ]] && IS_TTY=1
if [[ "$IS_TTY" == "1" ]]; then
    C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'
    C_BLUE=$'\033[34m'; C_DIM=$'\033[2m'; C_BOLD=$'\033[1m'; C_OFF=$'\033[0m'
else
    C_RED=''; C_GREEN=''; C_YELLOW=''; C_BLUE=''; C_DIM=''; C_BOLD=''; C_OFF=''
fi
info()  { printf '%s==>%s %s\n' "$C_BLUE" "$C_OFF" "$*"; }
ok()    { printf '%s[ok]%s %s\n' "$C_GREEN" "$C_OFF" "$*"; }
warn()  { printf '%s[!]%s %s\n' "$C_YELLOW" "$C_OFF" "$*"; }
err()   { printf '%s[x]%s %s\n' "$C_RED" "$C_OFF" "$*" >&2; }
dim()   { printf '%s%s%s\n' "$C_DIM" "$*" "$C_OFF"; }

prepare_logging() {
    mkdir -p "$LOG_DIR" "$TMP_ROOT"
    LOG_FILE="$LOG_DIR/update-$(date +%Y%m%d-%H%M%S).log"
    if ! TMP_DIR="$(mktemp -d "$TMP_ROOT/update.XXXXXX" 2>/dev/null)"; then
        printf '[x] 无法在 %s 下创建临时目录，请检查仓库目录权限\n' "$TMP_ROOT" >&2
        exit "$EXIT_FAILED"
    fi
    # 临时目录的兜底清理；详细收尾（stash 提示、日志路径）在 main 的 EXIT trap 里
    trap 'if [[ -n "$TMP_DIR" && -d "$TMP_DIR" ]]; then rm -rf "$TMP_DIR"; fi' EXIT
}

on_exit() {
    local rc=$?
    if [[ -n "$STASH_SHA" && "$STASH_RESTORED" != "1" ]]; then
        warn "本地未提交改动仍保存在 stash 里（未丢失）"
        dim "     恢复命令： git stash pop    # $STASH_SHA"
    fi
    if [[ -n "$TMP_DIR" && -d "$TMP_DIR" ]]; then
        rm -rf "$TMP_DIR"
    fi
    if [[ -n "$LOG_FILE" ]]; then
        dim "运行日志： ${LOG_FILE#"$ROOT"/}"
    fi
    return "$rc"
}

# ---------------------------------------------------------------------------
# 工具函数
# ---------------------------------------------------------------------------
fail_usage() {
    err "$1"
    exit "$EXIT_USAGE"
}

fail_run() {
    err "$1"
    dim "     工作区没有做改动；如需排查可把日志交给 agent： ${LOG_FILE#"$ROOT"/}"
    exit "$EXIT_FAILED"
}

short_sha() {
    git rev-parse --short "$1" 2>/dev/null || printf '%s' "$1"
}

version_at() {
    git show "$1:VERSION" 2>/dev/null | tr -d '[:space:]'
}

print_file_list() {
    # print_file_list <清单文件> [最多显示条数]
    local list="$1" limit="${2:-20}" total shown=0 line
    [[ -s "$list" ]] || return 0
    total="$(wc -l < "$list" | tr -d '[:space:]')"
    while IFS= read -r line; do
        [[ -n "$line" ]] || continue
        shown=$((shown + 1))
        if (( shown > limit )); then
            dim "     … 还有 $((total - limit)) 个文件，完整清单见日志"
            break
        fi
        printf '     - %s\n' "$line"
    done < "$list"
}

# 预检/更新中途停下时，把现场信息一次性打全，方便直接交给 agent
handoff() {
    local title="$1" reason="$2" list_file="${3:-}"
    local note="${4:-工作区已还原，没有留下半成品状态}"
    local behind_text ahead_text files_text="" total=0
    behind_text="${BEHIND:-?}"
    ahead_text="${AHEAD:-?}"

    printf '\n'
    printf '%s\n' "${C_YELLOW}================================================================${C_OFF}"
    printf '%s\n' "${C_YELLOW}  ${C_BOLD}${title}${C_OFF}"
    printf '%s\n' "${C_YELLOW}================================================================${C_OFF}"
    printf '原因：%s\n\n' "$reason"

    if [[ -n "$list_file" && -s "$list_file" ]]; then
        printf '涉及文件：\n'
        print_file_list "$list_file"
        printf '\n'
    fi

    printf '现场信息：\n'
    printf '   仓库        ： %s\n' "$ROOT"
    printf '   分支        ： %s（上游 %s/%s）\n' "${CURRENT_BRANCH:-未知}" "$REMOTE" "$BRANCH"
    if [[ -n "$NEW_SHA" ]]; then
        # 代码已经更新完，再把“更新前是什么样”写清楚，避免误判
        printf '   更新前 HEAD ： %s（当时落后 %s / 领先 %s）\n' \
            "$(short_sha "$LOCAL_SHA")" "$behind_text" "$ahead_text"
        printf '   当前 HEAD   ： %s（代码已更新到 %s/%s）\n' \
            "$(short_sha "$NEW_SHA")" "$REMOTE" "$BRANCH"
    else
        printf '   本地 HEAD   ： %s\n' "${LOCAL_SHA:+$(short_sha "$LOCAL_SHA")}"
        printf '   上游 HEAD   ： %s\n' "${REMOTE_SHA:+$(short_sha "$REMOTE_SHA")}"
        printf '   落后 / 领先 ： %s / %s 个提交\n' "$behind_text" "$ahead_text"
    fi
    if [[ -n "$BACKUP_REF" ]]; then
        printf '   备份引用    ： %s（更新前状态，可 git reset --keep 回退）\n' "$BACKUP_REF"
    fi
    if [[ -n "$STASH_SHA" ]]; then
        printf '   本地改动    ： 已暂存于 stash %s（未丢失）\n' "$STASH_SHA"
    fi
    printf '   运行日志    ： %s\n' "${LOG_FILE#"$ROOT"/}"
    printf '\n'

    # 交给 agent 的那句话要能直接照抄：文件太多时只列前 8 个，其余看日志
    if [[ -n "$list_file" && -s "$list_file" ]]; then
        total="$(wc -l < "$list_file" | tr -d '[:space:]')"
        files_text="$(head -8 "$list_file" | tr '\n' '、' | sed 's/、$//')"
        if (( total > 8 )); then
            files_text="${files_text}、…（共 ${total} 个）"
        fi
    fi

    printf '%s\n' "${C_BOLD}交给 agent 时，直接把下面这句发过去：${C_OFF}"
    printf '   「解决 %s 的代码更新问题。先读日志 %s，' "$ROOT" "${LOG_FILE#"$ROOT"/}"
    if [[ -n "$files_text" ]]; then
        printf '冲突/涉及文件：%s。' "$files_text"
    fi
    printf '请在 %s 分支上完成更新（同步 %s/%s），解决后跑必要验证并说明结果。」\n' \
        "$BRANCH" "$REMOTE" "$BRANCH"
    printf '\n%s\n' "${C_DIM}${note}${C_OFF}"
    exit "$EXIT_NEED_AGENT"
}

# ---------------------------------------------------------------------------
# 前置检查
# ---------------------------------------------------------------------------
preflight() {
    cd "$ROOT" || fail_usage "无法进入仓库目录：$ROOT"
    command -v git >/dev/null 2>&1 || fail_usage "未找到 git 命令"
    git rev-parse --is-inside-work-tree >/dev/null 2>&1 || fail_usage "不是 git 工作区：$ROOT"

    CURRENT_BRANCH="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || printf '')"
    if [[ "$CURRENT_BRANCH" == "HEAD" ]]; then
        fail_usage "当前处于 detached HEAD，请先 git switch $BRANCH 再更新"
    fi

    local pending="" name
    for name in MERGE_HEAD CHERRY_PICK_HEAD REVERT_HEAD; do
        [[ -e "$(git rev-parse --git-path "$name")" ]] && pending="$pending $name"
    done
    for name in rebase-merge rebase-apply; do
        [[ -d "$(git rev-parse --git-path "$name")" ]] && pending="$pending $name"
    done
    if [[ -n "$pending" ]]; then
        LOCAL_SHA="$(git rev-parse HEAD)"
        handoff "更新已停止：仓库里还有未结束的 git 操作" \
            "存在未完成的合并/变基/拣选状态（${pending}），脚本不会替你清理，需要 agent 先收尾"
    fi

    git remote get-url "$REMOTE" >/dev/null 2>&1 \
        || fail_usage "找不到远端 ${REMOTE}，检查 git remote -v
本脚本默认从官方远端 upstream 拉取（本仓库是官方仓库的 fork），需要的配置：
  git remote add upstream https://github.com/ddcat-ai/open-ai-canvas.git   # 官方，只拉取
  git remote set-url origin  https://github.com/zjhr/open-ai-canvas.git    # 自己，只推送
或者用环境变量指定其它远端名：CANVAS_UPDATE_REMOTE=... ./scripts/update-all.sh"

    # 拉取源和推送目标不能是同一个远端，否则 fork 场景下容易把官方当成自己的仓库
    if [[ "$REMOTE" == "$PUSH_REMOTE" ]]; then
        fail_usage "拉取远端和推送远端不能相同（都是 ${REMOTE}）。
请把官方仓库配成 upstream、自己的 fork 配成 origin，或显式设置 CANVAS_UPDATE_PUSH_REMOTE"
    fi

    if [[ "$MODE" != "check" && "$CURRENT_BRANCH" != "$BRANCH" ]]; then
        fail_usage "当前分支是 ${CURRENT_BRANCH}，更新脚本只操作 ${BRANCH}。请先 git switch ${BRANCH}（只看状态可以加 --check）"
    fi
}

# ---------------------------------------------------------------------------
# 拉取与状态计算
# ---------------------------------------------------------------------------
fetch_remote() {
    PRE_FETCH_REMOTE_SHA="$(git rev-parse -q --verify "refs/remotes/$REMOTE/$BRANCH" 2>/dev/null || printf '')"
    info "拉取 $REMOTE/$BRANCH ..."
    if ! git fetch --prune --no-tags "$REMOTE" "$BRANCH"; then
        fail_run "拉取失败（网络或代理问题）。没有任何文件被改动。"
    fi
    REMOTE_SHA="$(git rev-parse -q --verify "refs/remotes/$REMOTE/$BRANCH" 2>/dev/null || printf '')"
    [[ -n "$REMOTE_SHA" ]] || fail_run "远端不存在 $REMOTE/$BRANCH"

    # 上游强推：本地这个远端引用不再是新引用的祖先，说明历史被重写
    if [[ -n "$PRE_FETCH_REMOTE_SHA" && "$PRE_FETCH_REMOTE_SHA" != "$REMOTE_SHA" ]]; then
        if ! git merge-base --is-ancestor "$PRE_FETCH_REMOTE_SHA" "$REMOTE_SHA" 2>/dev/null; then
            UPSTREAM_REWRITTEN=1
        fi
    fi
}

compute_status() {
    LOCAL_SHA="$(git rev-parse HEAD)"
    BEHIND="$(git rev-list --count "HEAD..$REMOTE_SHA" 2>/dev/null || printf '0')"
    AHEAD="$(git rev-list --count "$REMOTE_SHA..HEAD" 2>/dev/null || printf '0')"

    tr '\0' '\n' < <(git -c core.quotepath=false diff --name-only -z "$LOCAL_SHA" "$REMOTE_SHA" 2>/dev/null || true) \
        | LC_ALL=C sort -u > "$TMP_DIR/upstream_files.txt"

    # 依赖相关文件的差异决定要不要重新安装依赖
    DEPS_CHANGED="$(git -c core.quotepath=false diff --name-only "$LOCAL_SHA" "$REMOTE_SHA" -- \
        web/package.json web/bun.lock web/bun.lockb \
        backend/go.mod backend/go.sum \
        backend/agent-runtime/pi/package.json backend/agent-runtime/pi/package-lock.json \
        2>/dev/null || true)"
}

show_state() {
    local local_version remote_version dirty
    local_version="$(version_at "$LOCAL_SHA")"
    remote_version="$(version_at "$REMOTE_SHA")"
    dirty="$(git status --porcelain | wc -l | tr -d '[:space:]')"

    printf '仓库：%s\n' "$ROOT"
    printf '分支：%s → %s/%s\n' "$CURRENT_BRANCH" "$REMOTE" "$BRANCH"
    printf '版本：%s → %s\n' "${local_version:-未知}" "${remote_version:-未知}"
    printf '提交：%s → %s（落后 %s，领先 %s）\n' \
        "$(short_sha "$LOCAL_SHA")" "$(short_sha "$REMOTE_SHA")" "$BEHIND" "$AHEAD"
    printf '工作区：%s 项未提交变更\n' "$dirty"
    printf '\n'

    if [[ "$BEHIND" == "0" && "$AHEAD" == "0" ]]; then
        return 0
    fi
    printf '上游新提交（最多显示 20 条）：\n'
    git --no-pager log --oneline --no-decorate -20 "$LOCAL_SHA..$REMOTE_SHA" | sed 's/^/   /'
    if (( BEHIND > 20 )); then
        dim "   … 还有 $((BEHIND - 20)) 条，完整列表： git log --oneline HEAD..$REMOTE/$BRANCH"
    fi
    printf '\n'
}

# ---------------------------------------------------------------------------
# 本地改动分析（会被上游覆盖的文件）
# ---------------------------------------------------------------------------
collect_local_changes() {
    {
        git -c core.quotepath=false diff --name-only -z HEAD
        git -c core.quotepath=false diff --cached --name-only -z
    } 2>/dev/null | tr '\0' '\n' | LC_ALL=C sort -u > "$TMP_DIR/local_dirty.txt" || true

    git ls-files --others --exclude-standard -z 2>/dev/null \
        | tr '\0' '\n' | LC_ALL=C sort -u > "$TMP_DIR/untracked.txt" || true

    git -c core.quotepath=false diff --name-only --diff-filter=A -z "$LOCAL_SHA" "$REMOTE_SHA" 2>/dev/null \
        | tr '\0' '\n' | LC_ALL=C sort -u > "$TMP_DIR/upstream_added.txt" || true

    # 本地已改动的文件里，哪些上游也改了 → 直接快进/合并会覆盖本地改动
    LC_ALL=C comm -12 "$TMP_DIR/local_dirty.txt" "$TMP_DIR/upstream_files.txt" \
        > "$TMP_DIR/overwrite.txt" || true

    # 本地未跟踪文件里，哪些上游新增了同名文件 → git 会拒绝更新
    LC_ALL=C comm -12 "$TMP_DIR/untracked.txt" "$TMP_DIR/upstream_added.txt" \
        > "$TMP_DIR/untracked_block.txt" || true
}

local_dirty_count() {
    wc -l < "$TMP_DIR/local_dirty.txt" | tr -d '[:space:]'
}

analyze_worktree() {
    collect_local_changes
    local dirty_count untracked_count
    dirty_count="$(local_dirty_count)"
    untracked_count="$(wc -l < "$TMP_DIR/untracked.txt" | tr -d '[:space:]')"

    if (( dirty_count > 0 || untracked_count > 0 )); then
        printf '本地未提交变更：%s 个已跟踪文件、%s 个未跟踪文件\n' "$dirty_count" "$untracked_count"
        print_file_list "$TMP_DIR/local_dirty.txt" 10
        if (( untracked_count > 0 )); then
            dim "     （未跟踪文件：$(head -3 "$TMP_DIR/untracked.txt" | tr '\n' ' ')）"
        fi
        printf '\n'
    fi

    if [[ -s "$TMP_DIR/overwrite.txt" && "$USE_STASH" != "1" ]]; then
        handoff "更新已停止：继续更新会覆盖你的本地改动" \
            "这些文件你本地改了、上游也改了；自动合并会丢掉你的改动，需要 agent 判断保留方式" \
            "$TMP_DIR/overwrite.txt"
    fi

    if [[ -s "$TMP_DIR/untracked_block.txt" && "$USE_STASH" != "1" ]]; then
        handoff "更新已停止：上游新增了与你本地未跟踪文件同名的文件" \
            "git 更新时会拒绝覆盖未跟踪文件，需要 agent 决定保留哪份" \
            "$TMP_DIR/untracked_block.txt"
    fi

    # 用了 --stash 又要合并同一批文件：更新能成，但恢复时必然冲突，先说清楚
    if [[ "$USE_STASH" == "1" && -s "$TMP_DIR/overwrite.txt" ]]; then
        warn "这些文件你本地改了、上游也改了：更新后恢复时会留下冲突标记，需要 agent 合并"
        print_file_list "$TMP_DIR/overwrite.txt" 10
        dim "     你的改动会安全留在 stash 里，不会丢"
    fi

    # rebase 要求干净工作区；脏工作区 + 本地提交只能交给用户或 --stash
    if (( dirty_count > 0 )) && [[ "$STRATEGY" == "rebase" ]] && (( AHEAD > 0 )) && [[ "$USE_STASH" != "1" ]]; then
        handoff "更新已停止：本地提交 + 未提交改动，脚本不擅自处理" \
            "变基需要干净工作区。可以：提交/暂存本地改动后重跑、加 --stash 让脚本自动处理，或交给 agent"
    fi
}

# ---------------------------------------------------------------------------
# 冲突试算（内存里做，不动工作区）
# ---------------------------------------------------------------------------
PREDICT=""
predict_conflicts() {
    if (( BEHIND == 0 )); then
        PREDICT="none"
        return 0
    fi
    if (( AHEAD == 0 )); then
        PREDICT="fast-forward"
        return 0
    fi

    local out rc=0
    out="$(git -c core.quotepath=false merge-tree --write-tree HEAD "$REMOTE_SHA" 2>&1)" || rc=$?
    printf '%s\n' "$out" > "$TMP_DIR/merge-tree.out"

    if (( rc == 0 )); then
        PREDICT="clean"
        return 0
    fi

    printf '%s\n' "$out" \
        | awk -F'\t' '$1 ~ /^[0-9]+ [0-9a-f]+ [123]$/ { print $2 }' \
        | LC_ALL=C sort -u > "$TMP_DIR/predict_conflicts.txt" || true

    if (( rc == 1 )) && [[ -s "$TMP_DIR/predict_conflicts.txt" ]]; then
        PREDICT="conflict"
        return 1
    fi

    # git 版本过旧、浅克隆等情况下试算不可用，退化为“更新后失败即回退”
    PREDICT="unknown"
    warn "无法预演合并（git merge-tree 不可用），将直接更新并在冲突时回退"
    return 0
}

# ---------------------------------------------------------------------------
# 应用更新
# ---------------------------------------------------------------------------
make_backup_ref() {
    BACKUP_REF="refs/canvas-update/backup-$(date +%Y%m%d-%H%M%S)"
    git update-ref "$BACKUP_REF" HEAD || fail_run "创建备份引用失败"
    ok "已记录更新前状态：${BACKUP_REF}（回退用： git reset --keep ${BACKUP_REF}）"
}

stash_local_changes() {
    if [[ "$USE_STASH" != "1" ]]; then
        return 0
    fi
    local dirty_count untracked_blocked=0 msg one
    local paths=()
    dirty_count="$(local_dirty_count)"
    if [[ -s "$TMP_DIR/untracked_block.txt" ]]; then
        untracked_blocked=1
    fi
    if (( dirty_count == 0 && untracked_blocked == 0 )); then
        return 0
    fi

    msg="canvas-update-$(date +%Y%m%d-%H%M%S)"
    info "暂存本地未提交改动 ..."
    if (( dirty_count > 0 && untracked_blocked == 1 )); then
        # 两类都要清：整包暂存（含未跟踪文件），一个 stash 管住
        if ! git stash push -u -m "$msg"; then
            fail_run "暂存本地改动失败"
        fi
    elif (( untracked_blocked == 1 )); then
        # 只收走挡住更新的那几个未跟踪文件，其它未跟踪文件不动
        while IFS= read -r one; do
            [[ -n "$one" ]] && paths+=("$one")
        done < "$TMP_DIR/untracked_block.txt"
        if (( ${#paths[@]} > 0 )); then
            if ! git stash push -u -m "$msg" -- "${paths[@]}"; then
                fail_run "暂存挡路的未跟踪文件失败"
            fi
        fi
    else
        if ! git stash push -m "$msg"; then
            fail_run "暂存本地改动失败"
        fi
    fi

    STASH_SHA="$(git rev-parse refs/stash)"
    ok "本地改动已暂存：${STASH_SHA}（更新后自动恢复）"
}

collect_unmerged() {
    git -c core.quotepath=false diff --name-only --diff-filter=U -z 2>/dev/null \
        | tr '\0' '\n' | LC_ALL=C sort -u > "$TMP_DIR/conflicts.txt" || true
}

# 冲突清单优先；没有 unmerged 文件时退回“会被覆盖”的清单，避免日志里只有一句空话
pick_handoff_list() {
    local candidate
    for candidate in "$TMP_DIR/conflicts.txt" "$TMP_DIR/predict_conflicts.txt" \
                     "$TMP_DIR/overwrite.txt" "$TMP_DIR/untracked_block.txt"; do
        if [[ -s "$candidate" ]]; then
            printf '%s' "$candidate"
            return 0
        fi
    done
    printf '%s' "$TMP_DIR/conflicts.txt"
}

abort_in_progress() {
    local git_dir
    git_dir="$(git rev-parse --absolute-git-dir 2>/dev/null || printf '')"
    if [[ -d "$git_dir/rebase-merge" || -d "$git_dir/rebase-apply" ]]; then
        git rebase --abort >/dev/null 2>&1 || return 1
    elif [[ -e "$git_dir/MERGE_HEAD" ]]; then
        git merge --abort >/dev/null 2>&1 || return 1
    fi
    return 0
}

apply_update() {
    if (( BEHIND == 0 )); then
        return 0
    fi
    if [[ "$UPSTREAM_REWRITTEN" == "1" ]]; then
        handoff "更新已停止：上游重写了历史（强推）" \
            "$REMOTE/$BRANCH 不再是本地跟踪引用的后继，自动变基/合并会制造重复提交，需要 agent 判断"
    fi
    if [[ "$STRATEGY" == "ff-only" ]] && (( AHEAD > 0 )); then
        handoff "更新已停止：本地有 $AHEAD 个提交，--ff-only 不做合并" \
            "本地提交与上游提交已经分叉，需要人工决定 rebase 还是 merge（或交给 agent）"
    fi

    make_backup_ref
    stash_local_changes

    if (( AHEAD == 0 )); then
        info "快进到 $REMOTE/$BRANCH ..."
        if ! git merge --ff-only "$REMOTE_SHA"; then
            collect_unmerged
            abort_in_progress || true
            handoff "更新已停止：快进失败" \
                "本地文件挡住了上游改动（或工作区状态不允许快进），工作区已还原" \
                "$(pick_handoff_list)"
        fi
    elif [[ "$STRATEGY" == "merge" ]]; then
        info "合并 $REMOTE/${BRANCH}（merge，保留本地提交）..."
        if ! git merge --no-edit "$REMOTE_SHA"; then
            collect_unmerged
            if ! abort_in_progress; then
                handoff "合并冲突，且自动回退失败" \
                    "合并出现冲突，git merge --abort 也没能还原；请不要继续手工操作，交给 agent"
            fi
            handoff "更新已停止：合并出现冲突" \
                "上游改动与本地提交冲突，已用 git merge --abort 还原到更新前状态" \
                "$(pick_handoff_list)"
        fi
    else
        info "变基到 $REMOTE/${BRANCH}（rebase 本地提交）..."
        if ! git rebase "$REMOTE_SHA"; then
            collect_unmerged
            if ! abort_in_progress; then
                handoff "变基冲突，且自动回退失败" \
                    "变基出现冲突，git rebase --abort 也没能还原；请不要继续手工操作，交给 agent" \
                    "$TMP_DIR/conflicts.txt"
            fi
            handoff "更新已停止：变基出现冲突" \
                "上游改动与本地提交冲突，已用 git rebase --abort 还原到更新前状态，本地提交完好" \
                "$(pick_handoff_list)"
        fi
    fi

    NEW_SHA="$(git rev-parse HEAD)"
    if [[ "$NEW_SHA" == "$LOCAL_SHA" ]]; then
        fail_run "更新后 HEAD 没有变化，git 状态异常"
    fi
    ok "代码已更新：$(short_sha "$LOCAL_SHA") → $(short_sha "$NEW_SHA")"
}

restore_stash() {
    [[ -n "$STASH_SHA" ]] || return 0
    info "恢复暂存的本地改动 ..."
    if git stash pop; then
        STASH_RESTORED=1
        ok "本地改动已恢复"
        return 0
    fi
    collect_unmerged
    STASH_RESTORED=0
    handoff "代码已更新，但恢复本地改动时冲突" \
        "代码已到最新；你之前的本地改动仍在 stash（${STASH_SHA}）里，工作区留下了冲突标记，需要 agent 合并" \
        "$(pick_handoff_list)" \
        "注意：代码已经是最新，只有本地改动还没合完；解决冲突后再确认是否 git stash drop。"
}

# ---------------------------------------------------------------------------
# 依赖同步与服务重启
# ---------------------------------------------------------------------------
resolve_bun() {
    if [[ -x "$LOCAL_DIR/tools/bun" ]]; then
        printf '%s' "$LOCAL_DIR/tools/bun"
        return 0
    fi
    if command -v bun >/dev/null 2>&1; then
        command -v bun
        return 0
    fi
    return 1
}

# 这组依赖文件这次更新里有没有变（--deps 时无条件同步）
deps_touched() {
    local pattern
    [[ -n "$DEPS_CHANGED" ]] || return 1
    for pattern in "$@"; do
        if printf '%s\n' "$DEPS_CHANGED" | grep -Fxq -- "$pattern"; then
            return 0
        fi
    done
    return 1
}

sync_deps() {
    local bun_bin=""
    DEPS_FAILED=0

    if [[ -f "$ROOT/web/package.json" ]] \
        && { [[ "$SYNC_DEPS" == "1" ]] || deps_touched web/package.json web/bun.lock web/bun.lockb; }; then
        if bun_bin="$(resolve_bun)"; then
            info "同步前端依赖（bun install --frozen-lockfile）..."
            if ( cd "$ROOT/web" && NPM_CONFIG_USERCONFIG=/dev/null "$bun_bin" install --frozen-lockfile ); then
                ok "前端依赖就绪"
            else
                err "前端依赖安装失败"
                DEPS_FAILED=1
            fi
        else
            warn "找不到 bun，跳过前端依赖同步"
        fi
    fi

    if [[ -f "$ROOT/backend/go.mod" ]] \
        && { [[ "$SYNC_DEPS" == "1" ]] || deps_touched backend/go.mod backend/go.sum; }; then
        if command -v go >/dev/null 2>&1; then
            info "同步后端依赖（go mod download）..."
            if ( cd "$ROOT/backend" \
                    && GOCACHE="$LOCAL_DIR/cache/go-build" \
                       GOMODCACHE="$LOCAL_DIR/cache/go-mod" \
                       GOPATH="$LOCAL_DIR/cache/go-path" \
                       go mod download ); then
                ok "后端依赖就绪"
            else
                err "后端依赖下载失败"
                DEPS_FAILED=1
            fi
        else
            warn "找不到 go，跳过后端依赖同步"
        fi
    fi

    local agent_runtime="$ROOT/backend/agent-runtime/pi"
    if [[ -f "$agent_runtime/package.json" ]] \
        && { [[ "$SYNC_DEPS" == "1" ]] \
             || deps_touched backend/agent-runtime/pi/package.json backend/agent-runtime/pi/package-lock.json; }; then
        if command -v npm >/dev/null 2>&1; then
            info "同步 Agent 运行时依赖（npm install）..."
            if ( cd "$agent_runtime" && NPM_CONFIG_USERCONFIG=/dev/null \
                    npm install --no-audit --no-fund --cache "$LOCAL_DIR/npm-cache" ); then
                ok "Agent 运行时依赖就绪"
            else
                warn "Agent 运行时依赖安装失败，云端 Agent 功能可能不可用"
            fi
        else
            warn "找不到 npm，跳过 Agent 运行时依赖同步"
        fi
    fi

    if (( DEPS_FAILED > 0 )); then
        return 1
    fi
    return 0
}

services_running() {
    local pid_file pid
    for pid_file in "$LOCAL_DIR/run/backend.pid" "$LOCAL_DIR/run/frontend.pid"; do
        [[ -f "$pid_file" ]] || continue
        pid="$(tr -d '[:space:]' < "$pid_file")"
        if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
            printf '%s' "$pid_file"
            return 0
        fi
    done
    return 1
}

post_update() {
    if [[ -n "$DEPS_CHANGED" ]]; then
        printf '依赖文件有变化：\n'
        printf '%s\n' "$DEPS_CHANGED" | sed 's/^/     - /'
        printf '\n'
    fi

    if [[ "$SYNC_DEPS" == "1" || ( "$DO_RESTART" == "1" && -n "$DEPS_CHANGED" ) ]]; then
        if ! sync_deps; then
            warn "依赖同步未全部成功，重启前建议先手动确认"
        fi
    elif [[ -n "$DEPS_CHANGED" ]]; then
        dim "     依赖有更新：重新安装可加 --deps，或手动 cd web && bun install --frozen-lockfile"
    fi

    if [[ "$DO_RESTART" == "1" ]]; then
        if [[ -x "$SCRIPT_DIR/start-all.sh" ]]; then
            info "重启本地服务（scripts/start-all.sh restart）..."
            if "$SCRIPT_DIR/start-all.sh" restart; then
                ok "本地服务已重启"
            else
                err "本地服务重启失败，见上面输出或 .local/logs/"
                DEPS_FAILED=1
            fi
        else
            warn "找不到 scripts/start-all.sh，跳过重启"
            dim "     手动重启： ./scripts/start-all.sh restart"
        fi
    elif services_running >/dev/null; then
        warn "检测到本地服务仍在运行，跑的是旧代码"
        dim "     重启： ./scripts/start-all.sh restart"
    fi
}

summary() {
    local count local_version remote_version
    count="$(git rev-list --count "$LOCAL_SHA..$NEW_SHA" 2>/dev/null || printf '0')"
    local_version="$(version_at "$LOCAL_SHA")"
    remote_version="$(version_at "$NEW_SHA")"

    printf '\n'
    printf '%s\n' "${C_GREEN}================================================================${C_OFF}"
    printf '%s\n' "${C_GREEN}  ${C_BOLD}更新完成${C_OFF}"
    printf '%s\n' "${C_GREEN}================================================================${C_OFF}"
    printf '   版本        ： %s → %s\n' "${local_version:-未知}" "${remote_version:-未知}"
    printf '   提交        ： %s → %s（%s 个新提交）\n' \
        "$(short_sha "$LOCAL_SHA")" "$(short_sha "$NEW_SHA")" "$count"
    printf '   合并自      ： %s/%s（官方）\n' "$REMOTE" "$BRANCH"
    printf '   变更文件    ： %s 个\n' "$(wc -l < "$TMP_DIR/upstream_files.txt" | tr -d '[:space:]')"
    if [[ -n "$STASH_SHA" ]]; then
        printf '   本地改动    ： 已恢复（原 stash %s）\n' "$STASH_SHA"
    fi
    printf '   备份引用    ： %s\n' "$BACKUP_REF"
    printf '   运行日志    ： %s\n' "${LOG_FILE#"$ROOT"/}"
    printf '\n'
    dim "  回退到更新前： git reset --keep $BACKUP_REF"
    dim "  推送到自己的仓库： git push ${PUSH_REMOTE} ${BRANCH}（或下次加 --push）"
}

# 把合并结果推送到自己的仓库（origin）。只推 main，绝不推官方 upstream。
push_own_remote() {
    [[ "$DO_PUSH" == "1" ]] || return 0
    if ! git remote get-url --push "$PUSH_REMOTE" >/dev/null 2>&1; then
        warn "找不到推送远端 ${PUSH_REMOTE}，跳过推送"
        dim "     手动推送： git push <你的仓库> $BRANCH"
        return 0
    fi
    info "推送 $BRANCH 到 ${PUSH_REMOTE} ..."
    if git push "$PUSH_REMOTE" "$BRANCH"; then
        ok "已推送到 ${PUSH_REMOTE}/$BRANCH"
    else
        warn "推送失败（网络/权限/非快进），本地提交不受影响"
        dim "     手动推送： git push $PUSH_REMOTE $BRANCH"
    fi
}

# ---------------------------------------------------------------------------
# 主流程
# ---------------------------------------------------------------------------
main() {
    # main 跑在管道左侧的子 shell 里（配合底部的 tee 留日志），
    # 收尾 trap 必须在这里装，才能看到 STASH_SHA 等运行状态
    trap on_exit EXIT
    preflight
    fetch_remote
    compute_status
    show_state

    if (( BEHIND == 0 )); then
        if (( AHEAD > 0 )); then
            ok "本地已经包含 $REMOTE/$BRANCH（官方）的全部提交（本地领先 $AHEAD 个提交）"
        else
            ok "已是最新代码：$(short_sha "$LOCAL_SHA")（官方 $REMOTE/${BRANCH}）"
        fi
        # 代码虽然没变，但本地可能还有没推送到自己仓库的提交
        if [[ "$MODE" == "apply" ]]; then
            push_own_remote
        fi
        exit "$EXIT_OK"
    fi

    if [[ "$MODE" == "check" ]]; then
        printf '发现 %s 个新提交（本地领先 %s 个）\n' "$BEHIND" "$AHEAD"
        collect_local_changes
        if [[ -s "$TMP_DIR/overwrite.txt" ]]; then
            warn "直接更新会覆盖你本地改过的文件（需要 agent 或先提交）："
            print_file_list "$TMP_DIR/overwrite.txt" 10
        fi
        if [[ -s "$TMP_DIR/untracked_block.txt" ]]; then
            warn "上游新增了与你本地未跟踪文件同名的文件："
            print_file_list "$TMP_DIR/untracked_block.txt" 10
        fi
        if [[ -n "$DEPS_CHANGED" ]]; then
            printf '依赖文件有变化（更新后建议加 --deps）：\n%s\n' "$(printf '%s\n' "$DEPS_CHANGED" | sed 's/^/     - /')"
        fi
        dim "     更新： ./scripts/update-all.sh（先预演可加 --dry-run）"
        exit "$EXIT_OK"
    fi

    analyze_worktree

    if ! predict_conflicts; then
        if [[ "$MODE" == "dry" ]]; then
            handoff "预演结果：直接更新会冲突（没有改动任何文件）" \
                "合并官方代码会在这几个文件上冲突，先交给 agent 处理更稳" \
                "$TMP_DIR/predict_conflicts.txt"
        fi
        handoff "更新已停止：预检发现冲突" \
            "提前试算（git merge-tree）发现官方改动与本地提交冲突，已停在工作区原状，没有开始合并" \
            "$TMP_DIR/predict_conflicts.txt"
    fi

    if [[ "$MODE" == "dry" ]]; then
        ok "预演通过：可以安全更新（${PREDICT}），未改动任何文件"
        dim "     正式更新： ./scripts/update-all.sh"
        exit "$EXIT_OK"
    fi

    apply_update
    restore_stash
    push_own_remote
    post_update
    summary

    if (( DEPS_FAILED > 0 )); then
        exit "$EXIT_FAILED"
    fi
    exit "$EXIT_OK"
}

# 不用 exec > >(tee ...)：受限环境里 /dev/fd 打不开，整脚本会直接失败
prepare_logging
set +e
main "$@" 2>&1 | tee -a "$LOG_FILE"
rc="${PIPESTATUS[0]}"
exit "$rc"
