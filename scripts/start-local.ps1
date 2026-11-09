# 影策本地开发启动脚本（Windows 宿主机）
#
# 在一键入口 scripts/start-canvas.ps1 之下负责真正拉起前后端。相对早先版本补齐了
# 本机必需的几项，否则"一键"会以各种方式失败：
#
#   1. 后端 sqlite 驱动需要 CGO。本机没有 C 编译器时 go 会以 CGO_ENABLED=0 构建，
#      进程启动即退出并报 "go-sqlite3 requires cgo to work. This is a stub"。
#      这里按 PATH → .local/tools/w64devkit 顺序找 gcc，都没有时下载便携版
#      w64devkit（约 64MB，解压到被 git 忽略的 .local/tools/），再以 CGO_ENABLED=1 启动。
#   2. 前端必须跑在 development。父环境若带 NODE_ENV=production，Vite 会走生产分支，
#      @vitejs/plugin-react 不注入 React Refresh 前置脚本，而 oxc 仍插入 $RefreshSig$，
#      结果首页白屏并抛 "ReferenceError: $RefreshSig$ is not defined"。
#   3. 提示词画廊同步要访问 huggingface.co。本机 hosts 把它指向 127.0.0.1（经本地代理
#      出口），后端出站 SSRF 校验会把解析结果判为内网而拒绝。按仓库约定用
#      CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS 精确放行该域名（不是关闭 SSRF），
#      传 -AllowedPrivateUpstreamHosts '' 可关闭。
#   4. 首次运行补齐依赖：web/node_modules（bun）与 backend/agent-runtime/pi（npm）。

[CmdletBinding()]
param(
    # 用已编译好的后端二进制启动，跳过 gcc/CGO（切换分支后需重新编译）。
    [string]$BackendExe = "",
    [int]$BackendPort = 8080,
    [int]$WebPort = 3000,
    # 逗号分隔的可信上游主机；这些主机允许解析到内网地址。留空表示不放行任何主机。
    [string]$AllowedPrivateUpstreamHosts = "huggingface.co",
    # 缺少 C 编译器时不自动下载便携版 w64devkit，只给出提示。
    [switch]$NoGccBootstrap,
    # 便携 w64devkit 压缩包的 SHA256；默认是该版本首次下载核对过的值，传 '' 可跳过校验。
    [string]$GccArchiveSha256 = "ADD1EDF984CE4D473CFEB7C52E0AB38DB73A855BBA6C7F564D5E16648EB2896A",
    # 不安装 backend/agent-runtime/pi 的 npm 依赖（云端 Agent 会降级）。
    [switch]$SkipAgentRuntimeDeps
)

$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "windows-proxy.ps1")
Import-CanvasWindowsProxy

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$backendDir = Join-Path $repoRoot "backend"
$webDir = Join-Path $repoRoot "web"
$localDir = Join-Path $repoRoot ".local"
$dataDir = Join-Path $localDir "project-workbench-debug"
$goBuildCache = Join-Path $localDir "cache\go-build"
$goModuleCache = Join-Path $localDir "cache\go-mod"
$toolsDir = Join-Path $localDir "tools"
$portableGcc = Join-Path $toolsDir "w64devkit\bin\gcc.exe"

# 便携 C 编译器：仓库不缓存二进制，只固定版本号，首次运行下载到 .local/tools/。
$w64devkitVersion = "2.10.1"
$w64devkitArchive = "w64devkit-x64-$w64devkitVersion.7z.exe"
$w64devkitUrl = "https://github.com/skeeto/w64devkit/releases/download/v$w64devkitVersion/$w64devkitArchive"

function Write-Step([string]$Message) { Write-Host $Message -ForegroundColor Cyan }
function Write-Warn([string]$Message) { Write-Host $Message -ForegroundColor Yellow }

foreach ($commandName in @("go", "bun")) {
    if (-not (Get-Command $commandName -ErrorAction SilentlyContinue)) {
        throw "未找到 $commandName，请先安装项目要求的运行时。"
    }
}

foreach ($directory in @($dataDir, $goBuildCache, $goModuleCache)) {
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
}

function Resolve-CgoCompiler {
    if ($BackendExe) {
        $resolved = (Resolve-Path -LiteralPath $BackendExe -ErrorAction SilentlyContinue)
        if (-not $resolved) { throw "指定的后端二进制不存在：$BackendExe" }
        return @{ Path = $null; BackendCommand = "& '$(($resolved.Path).Replace("'", "''"))'" }
    }

    foreach ($candidate in @("gcc", "cc", "clang")) {
        $found = Get-Command $candidate -ErrorAction SilentlyContinue
        if ($found) {
            Write-Host "C 编译器：$($found.Source)"
            return @{ Path = $found.Source; BackendCommand = "go run ./cmd/server" }
        }
    }

    if (Test-Path -LiteralPath $portableGcc) {
        Write-Host "C 编译器：$portableGcc（便携版 w64devkit）"
        return @{ Path = $portableGcc; BackendCommand = "go run ./cmd/server" }
    }

    if ($NoGccBootstrap) {
        throw "未找到 C 编译器，后端 sqlite 需要 CGO。请安装 MinGW-w64/gcc 后重试，或去掉 -NoGccBootstrap 让脚本自动下载便携版。"
    }

    Write-Warn "未找到 C 编译器，后端 sqlite 需要 CGO：将下载便携版 w64devkit v$w64devkitVersion（约 64MB）到 .local\tools\"
    New-Item -ItemType Directory -Force -Path $toolsDir | Out-Null
    $archivePath = Join-Path $toolsDir $w64devkitArchive
    $ProgressPreference = "SilentlyContinue"
    if (-not (Test-Path -LiteralPath $archivePath)) {
        Invoke-WebRequest -Uri $w64devkitUrl -OutFile $archivePath -TimeoutSec 900 -UseBasicParsing
    }
    $sha256 = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash
    Write-Host "w64devkit 包 SHA256：$sha256"
    if ($GccArchiveSha256 -and $sha256 -ne $GccArchiveSha256.ToUpperInvariant()) {
        throw "w64devkit 压缩包校验失败：期望 $($GccArchiveSha256.ToUpperInvariant())，实际 $sha256。请删除 $archivePath 后重试，或用 -GccArchiveSha256 '' 跳过校验。"
    }
    Write-Warn "自解压包来自 GitHub 官方 release；已按内置 SHA256 校验通过。"

    # 7z 自解压包：-y 静默、-o 指定目录；它自己带顶层 w64devkit 目录。
    # 不依赖退出码（该 SFX 的退出码不可靠），以 gcc.exe 是否落地为准。
    & $archivePath -y "-o$toolsDir" | Out-Null
    $deadline = (Get-Date).AddMinutes(3)
    while ((Get-Date) -lt $deadline -and -not (Test-Path -LiteralPath $portableGcc)) {
        Start-Sleep -Seconds 2
    }
    if (-not (Test-Path -LiteralPath $portableGcc)) {
        throw "解压 w64devkit 失败，未生成 $portableGcc。可手动解压 $archivePath 到 $toolsDir 后重试。"
    }
    Write-Host "便携 C 编译器就绪：$portableGcc"
    return @{ Path = $portableGcc; BackendCommand = "go run ./cmd/server" }
}

$compiler = Resolve-CgoCompiler

# Bun 生成的是 vite.exe / vite.bunx，没有无扩展名的 .bin/vite，别再按那个名字探测。
function Test-WebDependencies {
    $binDir = Join-Path $webDir "node_modules\.bin"
    if (-not (Test-Path -LiteralPath $binDir)) { return $false }
    return $null -ne (Get-ChildItem -LiteralPath $binDir -Filter "vite*" -ErrorAction SilentlyContinue | Select-Object -First 1)
}

if (-not (Test-WebDependencies)) {
    Write-Warn "web/node_modules 不完整，正在执行 bun install..."
    Push-Location $webDir
    try {
        & bun install
        if ($LASTEXITCODE -ne 0) { throw "bun install 失败，无法启动前端。" }
    } finally {
        Pop-Location
    }
}

# 嵌入模式下后端会在本进程内拉起 Node Agent runtime，缺依赖时该能力降级。
$piDir = Join-Path $backendDir "agent-runtime\pi"
if (-not $SkipAgentRuntimeDeps -and (Test-Path -LiteralPath (Join-Path $piDir "package.json")) -and -not (Test-Path -LiteralPath (Join-Path $piDir "node_modules"))) {
    if (Get-Command npm -ErrorAction SilentlyContinue) {
        Write-Warn "正在安装 backend/agent-runtime/pi 依赖（云端 Agent 需要）..."
        Push-Location $piDir
        try { & npm install --no-audit --no-fund } finally { Pop-Location }
        if ($LASTEXITCODE -ne 0) { Write-Warn "npm install 失败，云端 Agent 可能不可用；其余功能不受影响。" }
    } else {
        Write-Warn "未找到 npm，跳过 backend/agent-runtime/pi 依赖安装（云端 Agent 可能不可用）。"
    }
}

function Test-ListeningPort([int]$Port) {
    try {
        return $null -ne (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction Stop | Select-Object -First 1)
    } catch {
        return $false
    }
}

foreach ($port in @($WebPort, $BackendPort)) {
    if (Test-ListeningPort $port) {
        throw "端口 $port 已被占用，请先关闭占用进程后重试（scripts/start-canvas.ps1 在服务已就绪时会直接打开浏览器）。"
    }
}

$powerShellPath = (Get-Command pwsh -ErrorAction SilentlyContinue).Source
if (-not $powerShellPath) {
    $powerShellPath = (Get-Command powershell.exe -ErrorAction SilentlyContinue).Source
}
if (-not $powerShellPath) {
    throw "未找到 PowerShell，无法打开前后端独立窗口。"
}

function ConvertTo-PowerShellLiteral([string]$Value) {
    return "'" + $Value.Replace("'", "''") + "'"
}

# 后端窗口：CGO + 编译器 + 数据目录 + 出站白名单。
$backendEnvLines = @(
    "`$env:CANVAS_BACKEND_ADDR = '127.0.0.1:$BackendPort'",
    "`$env:CANVAS_BACKEND_DATA_DIR = $(ConvertTo-PowerShellLiteral $dataDir)",
    "`$env:GOCACHE = $(ConvertTo-PowerShellLiteral $goBuildCache)",
    "`$env:GOMODCACHE = $(ConvertTo-PowerShellLiteral $goModuleCache)",
    "`$env:CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS = $(ConvertTo-PowerShellLiteral $AllowedPrivateUpstreamHosts)"
)
if ($compiler.Path) {
    $gccBinDir = Split-Path -Parent $compiler.Path
    $backendEnvLines += "`$env:PATH = $(ConvertTo-PowerShellLiteral $gccBinDir) + ';' + `$env:PATH"
    $backendEnvLines += "`$env:CGO_ENABLED = '1'"
    $backendEnvLines += "`$env:CC = $(ConvertTo-PowerShellLiteral $compiler.Path)"
}

$backendCommand = @"
# canvas-local-dev:backend
`$ErrorActionPreference = 'Stop'
`$Host.UI.RawUI.WindowTitle = '影策后端 (canvas-local-dev)'
Set-Location -LiteralPath $(ConvertTo-PowerShellLiteral $backendDir)
$($backendEnvLines -join "`n")
Write-Host '影策后端：http://127.0.0.1:$BackendPort' -ForegroundColor Cyan
$($compiler.BackendCommand)
"@

# 前端窗口：强制 development，否则 NODE_ENV=production 会让首页白屏。
$webCommand = @"
# canvas-local-dev:frontend
`$ErrorActionPreference = 'Stop'
`$Host.UI.RawUI.WindowTitle = '影策前端 (canvas-local-dev)'
Set-Location -LiteralPath $(ConvertTo-PowerShellLiteral $webDir)
`$env:NODE_ENV = 'development'
`$env:VITE_API_PROXY_TARGET = 'http://127.0.0.1:$BackendPort'
Write-Host '影策前端：http://localhost:$WebPort' -ForegroundColor Cyan
bun run dev
"@

# 本会话若带着重复大小写的 NO_PROXY/no_proxy，Start-Process 复制环境时会报重复键。
Remove-Item Env:no_proxy -ErrorAction SilentlyContinue

$backendProcess = Start-Process -FilePath $powerShellPath -WindowStyle Normal -WorkingDirectory $backendDir -PassThru -ArgumentList @("-NoLogo", "-NoExit", "-NoProfile", "-Command", $backendCommand)
$webProcess = Start-Process -FilePath $powerShellPath -WindowStyle Normal -WorkingDirectory $webDir -PassThru -ArgumentList @("-NoLogo", "-NoExit", "-NoProfile", "-Command", $webCommand)

Write-Host "已打开前后端开发窗口。" -ForegroundColor Green
Write-Host "后端窗口 PID: $($backendProcess.Id)；前端窗口 PID: $($webProcess.Id)"
Write-Host "访问 http://localhost:$WebPort（后端健康检查 http://127.0.0.1:$BackendPort/api/health）"
if ($AllowedPrivateUpstreamHosts) {
    Write-Host "已精确放行上游主机：$AllowedPrivateUpstreamHosts（供提示词画廊同步访问 huggingface.co）" -ForegroundColor DarkGray
}
Write-Host "分别在两个窗口按 Ctrl+C 停止服务。"
