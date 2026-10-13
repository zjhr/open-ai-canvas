#!/usr/bin/env bash
#
# 影策（open-ai-canvas）本地一键启动 / 停止 / 重启脚本（宿主机，非 Docker）
#
#   ./scripts/start-all.sh          启动全部服务（已在运行则跳过）
#   ./scripts/start-all.sh status   查看状态
#   ./scripts/start-all.sh stop     停止全部服务
#   ./scripts/start-all.sh restart  重启全部服务
#   ./scripts/start-all.sh logs     跟随查看日志
#
# 设计要点（都是本机实际踩过的坑，改动前请先看这里）：
#   1. 系统 Bun 1.3.0 在本机无法安装依赖（写临时目录被拒），
#      因此优先使用仓库内 .local/tools/bun，其次才回退系统 bun。
#   2. Go 缓存默认写在 ~/go / ~/Library/Caches，本机这些路径不可写，
#      因此 GOCACHE / GOMODCACHE / GOPATH 统一定向到 .local/cache。
#   3. 本机环境存在损坏的 GIT_CONFIG_COUNT，会让 git 及 go 的 VCS stamping
#      直接报错；脚本内统一清掉这几个变量，并用 -buildvcs=false 编译。
#   4. 后端二进制比源码旧时自动重新编译，避免改了 Go 代码却跑旧版本。
#   5. 进程记 PID 到 .local/run/*.pid，停止时只杀自己启动的进程，
#      不用 pkill 模糊匹配（按名字杀会误伤 Vite 等其它进程）。
#
set -euo pipefail

# ---------------------------------------------------------------------------
# 基础路径
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
LOCAL_DIR="$ROOT/.local"
RUN_DIR="$LOCAL_DIR/run"
LOG_DIR="$LOCAL_DIR/logs"
BIN_DIR="$LOCAL_DIR/bin"

DATA_DIR="$LOCAL_DIR/project-workbench-debug"
GO_BUILD_CACHE="$LOCAL_DIR/cache/go-build"
GO_MOD_CACHE="$LOCAL_DIR/cache/go-mod"
GO_PATH="$LOCAL_DIR/cache/go-path"
NPM_CACHE="$LOCAL_DIR/npm-cache"

BACKEND_BIN="$BIN_DIR/canvas-server"
BACKEND_PID="$RUN_DIR/backend.pid"
FRONTEND_PID="$RUN_DIR/frontend.pid"
BACKEND_LOG="$LOG_DIR/backend.log"
FRONTEND_LOG="$LOG_DIR/frontend.log"

BACKEND_HOST="127.0.0.1"
BACKEND_PORT="${CANVAS_BACKEND_PORT:-8080}"
FRONTEND_PORT="${CANVAS_WEB_PORT:-3000}"

BACKEND_URL="http://$BACKEND_HOST:$BACKEND_PORT"

# 损坏的 GIT 环境变量会让 git / go build 直接失败，脚本内一律清掉
unset GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_KEY_1 2>/dev/null || true
unset GIT_CONFIG_VALUE_0 GIT_CONFIG_VALUE_1 2>/dev/null || true

# ---------------------------------------------------------------------------
# 输出
# ---------------------------------------------------------------------------
if [[ -t 1 ]]; then
    C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'
    C_BLUE=$'\033[34m'; C_DIM=$'\033[2m'; C_OFF=$'\033[0m'
else
    C_RED=''; C_GREEN=''; C_YELLOW=''; C_BLUE=''; C_DIM=''; C_OFF=''
fi
info()  { printf '%s==>%s %s\n' "$C_BLUE" "$C_OFF" "$*"; }
ok()    { printf '%s[ok]%s %s\n' "$C_GREEN" "$C_OFF" "$*"; }
warn()  { printf '%s[!]%s %s\n' "$C_YELLOW" "$C_OFF" "$*"; }
err()   { printf '%s[x]%s %s\n' "$C_RED" "$C_OFF" "$*" >&2; }
dim()   { printf '%s%s%s\n' "$C_DIM" "$*" "$C_OFF"; }

# ---------------------------------------------------------------------------
# 工具函数
# ---------------------------------------------------------------------------
port_pid() {
    # 监听指定端口的 PID（可能为空）
    lsof -nP -iTCP:"$1" -sTCP:LISTEN -t 2>/dev/null | head -1
}

is_pid_alive() {
    local pid="${1:-}"
    [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null
}

read_pid() {
    local file="$1"
    [[ -f "$file" ]] || return 1
    local pid
    pid="$(tr -d '[:space:]' < "$file")"
    [[ -n "$pid" ]] || return 1
    printf '%s' "$pid"
}

wait_http() {
    # wait_http <url> <超时秒> <描述>
    local url="$1" timeout="${2:-60}" label="${3:-服务}"
    local deadline=$((SECONDS + timeout))
    while (( SECONDS < deadline )); do
        if curl -fsS -m 3 -o /dev/null "$url" 2>/dev/null; then
            return 0
        fi
        sleep 1
    done
    return 1
}

# 解析可用的 bun：优先仓库内自带的，其次系统 PATH
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

ensure_dirs() {
    mkdir -p "$RUN_DIR" "$LOG_DIR" "$BIN_DIR" "$DATA_DIR" \
             "$GO_BUILD_CACHE" "$GO_MOD_CACHE" "$GO_PATH" "$NPM_CACHE"
}

# ---------------------------------------------------------------------------
# 前置检查
# ---------------------------------------------------------------------------
check_requirements() {
    local missing=()

    command -v go >/dev/null 2>&1 || missing+=("go")
    command -v node >/dev/null 2>&1 || missing+=("node")
    command -v curl >/dev/null 2>&1 || missing+=("curl")

    local bun_bin=""
    if ! bun_bin="$(resolve_bun)"; then
        missing+=("bun")
    fi

    if (( ${#missing[@]} > 0 )); then
        err "缺少运行时：${missing[*]}"
        echo "  项目需要 Go 1.25+、Node 22+、Bun。安装说明见 README「环境要求」。" >&2
        exit 1
    fi

    # node 版本（内嵌 Agent 运行时要求 >= 22.19）
    local node_major
    node_major="$(node -p 'process.versions.node.split(".")[0]' 2>/dev/null || echo 0)"
    if (( node_major < 22 )); then
        warn "Node 版本为 $(node --version)，Agent 运行时要求 >= 22.19，相关功能可能不可用"
    fi

    printf '%s' "$bun_bin"
}

# ---------------------------------------------------------------------------
# 依赖安装（仅在缺失时执行）
# ---------------------------------------------------------------------------
ensure_web_deps() {
    local bun_bin="$1"
    if [[ -d "$ROOT/web/node_modules" && -x "$ROOT/web/node_modules/.bin/vite" ]]; then
        return 0
    fi
    info "web/node_modules 缺失，执行 bun install --frozen-lockfile ..."
    (
        cd "$ROOT/web"
        # NPM_CONFIG_USERCONFIG=/dev/null 跳过本机 ~/.npmrc 里 bun 不支持的 :email 配置
        NPM_CONFIG_USERCONFIG=/dev/null "$bun_bin" install --frozen-lockfile
    ) || { err "前端依赖安装失败"; return 1; }
    ok "前端依赖就绪"
}

ensure_agent_runtime_deps() {
    local runtime_dir="$ROOT/backend/agent-runtime/pi"
    if [[ -d "$runtime_dir/node_modules/@earendil-works/pi-coding-agent" ]]; then
        return 0
    fi
    [[ -f "$runtime_dir/package.json" ]] || return 0
    info "Agent 运行时依赖缺失，执行 npm install ..."
    (
        cd "$runtime_dir"
        NPM_CONFIG_USERCONFIG=/dev/null npm install --no-audit --no-fund --cache "$NPM_CACHE"
    ) || { warn "Agent 运行时依赖安装失败，云端 Agent 功能将不可用"; return 0; }
    ok "Agent 运行时依赖就绪"
}

# ---------------------------------------------------------------------------
# 后端
# ---------------------------------------------------------------------------
backend_needs_build() {
    [[ -x "$BACKEND_BIN" ]] || return 0
    # 任一 .go / go.mod / go.sum 比二进制新，就需要重编
    if [[ -n "$(find "$ROOT/backend" \
            \( -name '*.go' -o -name 'go.mod' -o -name 'go.sum' \) \
            -newer "$BACKEND_BIN" -print -quit 2>/dev/null)" ]]; then
        return 0
    fi
    return 1
}

build_backend() {
    info "编译后端 ..."
    (
        cd "$ROOT/backend"
        export GOCACHE="$GO_BUILD_CACHE"
        export GOMODCACHE="$GO_MOD_CACHE"
        export GOPATH="$GO_PATH"
        # 不设置 GOFLAGS=-mod=mod：那会把 go.mod 中 // indirect 的依赖
        # 重新归类为直接依赖，污染工作区。保持默认 -mod=readonly。
        # -buildvcs=false：本机 git 环境异常，跳过 VCS stamping
        go build -buildvcs=false -o "$BACKEND_BIN" ./cmd/server
    ) || { err "后端编译失败"; return 1; }
    ok "后端编译完成"
}

start_backend() {
    local pid
    if pid="$(read_pid "$BACKEND_PID")" && is_pid_alive "$pid"; then
        ok "后端已在运行（PID ${pid}）"
        return 0
    fi

    local occupied
    if occupied="$(port_pid "$BACKEND_PORT")"; then
        err "端口 $BACKEND_PORT 已被其它进程占用（PID ${occupied}），不自动结束它"
        dim "     如需强制释放： kill $occupied"
        return 1
    fi

    backend_needs_build && { build_backend || return 1; }

    info "启动后端（$BACKEND_HOST:${BACKEND_PORT}）..."
    (
        cd "$ROOT/backend"
        export CANVAS_BACKEND_ADDR="$BACKEND_HOST:$BACKEND_PORT"
        export CANVAS_BACKEND_DATA_DIR="$DATA_DIR"
        export CANVAS_CORS_ORIGINS="http://localhost:$FRONTEND_PORT,http://127.0.0.1:$FRONTEND_PORT"
        # 本地开发不设置 YINGCE_AGENT_URL：后端在进程内启动 Node，无需单独跑 yingce-agent
        export GOCACHE="$GO_BUILD_CACHE"
        export GOMODCACHE="$GO_MOD_CACHE"
        export GOPATH="$GO_PATH"
        nohup "$BACKEND_BIN" >> "$BACKEND_LOG" 2>&1 < /dev/null &
        echo $! > "$BACKEND_PID"
    )

    pid="$(read_pid "$BACKEND_PID" || true)"
    if wait_http "$BACKEND_URL/api/health" 90 "后端"; then
        ok "后端就绪：$BACKEND_URL/api/health（PID ${pid:-未知}）"
        return 0
    fi
    err "后端在 90 秒内未就绪，日志尾部："
    tail -n 25 "$BACKEND_LOG" >&2 || true
    return 1
}

# ---------------------------------------------------------------------------
# 前端
# ---------------------------------------------------------------------------
start_frontend() {
    local bun_bin="$1"
    local pid
    if pid="$(read_pid "$FRONTEND_PID")" && is_pid_alive "$pid"; then
        ok "前端已在运行（PID ${pid}）"
        return 0
    fi

    local occupied
    if occupied="$(port_pid "$FRONTEND_PORT")"; then
        err "端口 $FRONTEND_PORT 已被其它进程占用（PID ${occupied}），不自动结束它"
        dim "     如需强制释放： kill $occupied"
        return 1
    fi

    info "启动前端（0.0.0.0:${FRONTEND_PORT}）..."
    (
        cd "$ROOT/web"
        # 供 Vite 代理到上面启动的后端
        export VITE_API_PROXY_TARGET="$BACKEND_URL"
        nohup "$bun_bin" run dev -- --port "$FRONTEND_PORT" \
            >> "$FRONTEND_LOG" 2>&1 < /dev/null &
        echo $! > "$FRONTEND_PID"
    )

    pid="$(read_pid "$FRONTEND_PID" || true)"
    if wait_http "http://127.0.0.1:$FRONTEND_PORT/" 90 "前端"; then
        ok "前端就绪：http://localhost:${FRONTEND_PORT}（PID ${pid:-未知}）"
        return 0
    fi
    err "前端在 90 秒内未就绪，日志尾部："
    tail -n 25 "$FRONTEND_LOG" >&2 || true
    return 1
}

# ---------------------------------------------------------------------------
# 停止
# ---------------------------------------------------------------------------
stop_one() {
    # stop_one <pid文件> <名字> <端口>
    local pidfile="$1" name="$2" port="${3:-}"
    local pid
    if ! pid="$(read_pid "$pidfile")"; then
        dim "  ${name}：无 PID 记录，跳过"
        rm -f "$pidfile"
        return 0
    fi
    if ! is_pid_alive "$pid"; then
        dim "  ${name}：进程 $pid 已不存在，清理 PID 记录"
        rm -f "$pidfile"
        return 0
    fi

    kill "$pid" 2>/dev/null || true
    local deadline=$((SECONDS + 15))
    while (( SECONDS < deadline )) && is_pid_alive "$pid"; do
        sleep 0.3
    done
    if is_pid_alive "$pid"; then
        warn "  ${name}（PID ${pid}）未在 15 秒内退出，强制结束"
        kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$pidfile"
    ok "  $name 已停止（PID ${pid}）"
}

cmd_stop() {
    info "停止服务 ..."
    stop_one "$FRONTEND_PID" "前端"
    stop_one "$BACKEND_PID" "后端"
}

# ---------------------------------------------------------------------------
# 状态
# ---------------------------------------------------------------------------
cmd_status() {
    local pid

    printf '\n%s影策服务状态%s\n' "$C_BLUE" "$C_OFF"

    # 后端
    if pid="$(read_pid "$BACKEND_PID")" && is_pid_alive "$pid"; then
        local health
        if health="$(curl -fsS -m 3 "$BACKEND_URL/api/health" 2>/dev/null)"; then
            local schema
            schema="$(printf '%s' "$health" | sed -n 's/.*"current":\([0-9]*\).*"expected":\([0-9]*\).*/\1\/\2/p')"
            ok "后端  运行中  PID $pid  $BACKEND_URL  (schema ${schema:-?})"
        else
            warn "后端  进程在（PID ${pid}）但健康检查失败，检查 $BACKEND_LOG"
        fi
    elif [[ -n "$(port_pid "$BACKEND_PORT")" ]]; then
        warn "后端  端口 $BACKEND_PORT 被 PID $(port_pid "$BACKEND_PORT") 占用（非本脚本启动）"
    else
        dim "后端  未运行"
    fi

    # 前端
    if pid="$(read_pid "$FRONTEND_PID")" && is_pid_alive "$pid"; then
        ok "前端  运行中  PID $pid  http://localhost:$FRONTEND_PORT"
    elif [[ -n "$(port_pid "$FRONTEND_PORT")" ]]; then
        warn "前端  端口 $FRONTEND_PORT 被 PID $(port_pid "$FRONTEND_PORT") 占用（非本脚本启动）"
    else
        dim "前端  未运行"
    fi

    # 数据
    local db="$DATA_DIR/open_ai_canvas.db"
    if [[ -f "$db" ]]; then
        dim "数据库  ${db}（$(du -h "$db" | cut -f1)）"
    else
        dim "数据库  尚未创建（首次启动后生成）"
    fi
    echo
}

cmd_logs() {
    local which="${1:-}"
    case "$which" in
        backend)  info "跟随后端日志（Ctrl-C 退出）"; tail -f "$BACKEND_LOG" ;;
        frontend) info "跟随前端日志（Ctrl-C 退出）"; tail -f "$FRONTEND_LOG" ;;
        *)
            warn "同时跟随前后端日志（Ctrl-C 退出）"
            tail -f "$BACKEND_LOG" "$FRONTEND_LOG"
            ;;
    esac
}

# ---------------------------------------------------------------------------
# 启动
# ---------------------------------------------------------------------------
cmd_start() {
    echo
    printf '%s影策 · 本地一键启动%s\n' "$C_BLUE" "$C_OFF"
    dim  "仓库：$ROOT"
    echo

    ensure_dirs

    local bun_bin
    bun_bin="$(check_requirements)"
    dim "bun  ：${bun_bin}（$("$bun_bin" --version 2>/dev/null || echo '?')）"
    if [[ "$bun_bin" == "$LOCAL_DIR/tools/bun" ]]; then
        dim "        使用仓库内 Bun（系统 Bun 1.3.0 在本机无法安装依赖）"
    fi
    dim "go   ：$(go version 2>/dev/null | awk '{print $3}')"
    echo

    ensure_web_deps "$bun_bin"
    ensure_agent_runtime_deps

    start_backend || exit 1
    start_frontend "$bun_bin" || exit 1

    echo
    ok "全部服务已启动"
    printf '\n  打开：%shttp://localhost:%s%s\n' "$C_GREEN" "$FRONTEND_PORT" "$C_OFF"
    dim  "  后端：$BACKEND_URL/api/health"
    dim  "  登录：见 .local/verify-credentials.env"
    dim  "  日志：$LOG_DIR/{backend,frontend}.log"
    echo
    dim  "  常用： $0 status | stop | restart | logs"
    echo
}

cmd_restart() {
    cmd_stop
    echo
    cmd_start
}

# ---------------------------------------------------------------------------
# 入口
# ---------------------------------------------------------------------------
case "${1:-start}" in
    start)   cmd_start ;;
    stop)    cmd_stop ;;
    restart) cmd_restart ;;
    status)  cmd_status ;;
    logs)    shift; cmd_logs "${1:-}" ;;
    -h|--help|help)
        cat <<EOF
影策（open-ai-canvas）本地服务管理脚本（宿主机，非 Docker）

用法：$0 [命令]

  start     启动全部服务（默认；已在运行则跳过，不会重复启动）
  stop      停止本脚本启动的服务
  restart   重启全部服务
  status    查看运行状态、端口、数据库
  logs [backend|frontend]   跟随日志

环境变量（可选）：
  CANVAS_BACKEND_PORT   后端端口，默认 8080
  CANVAS_WEB_PORT       前端端口，默认 3000

数据与日志均位于 ${LOCAL_DIR}（已被 .gitignore 忽略）。
EOF
        ;;
    *)
        err "未知命令：$1"
        echo "运行 '$0 help' 查看用法。" >&2
        exit 2
        ;;
esac
