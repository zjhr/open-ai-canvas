# 影策本地开发停止脚本（Windows 宿主机）
#
# 处理本项目 start-local.ps1 拉起的开发服务，分三步，越往后越保守：
#   1. 定位开发窗口：优先命令行标记 canvas-local-dev:backend|frontend；老版本窗口没有标记时，
#      退回到"命令行里同时出现本仓库路径和本项目特征变量"的 powershell 窗口。
#      默认先请求窗口正常关闭（CloseMainWindow），让 go/vite 有机会走退出流程；
#      窗口不响应再深度优先结束整棵进程树。
#   2. 端口仍被占用时，只在占用者命令行确实像本项目服务
#      （go run ./cmd/server、go-build 临时目录里的 server.exe、vite、bun run dev、yingce-server.exe）
#      时才结束它，并顺链收掉残留的启动壳。
#   3. 认不出来的占用者一律不杀，只报告，避免误伤别人的程序。
#
# 退出码：0 = 两个端口都已释放；1 = 仍有端口被占用。

[CmdletBinding()]
param(
    [int]$BackendPort = 8080,
    [int]$WebPort = 3000,
    # 跳过温和关闭，直接结束进程树。
    [switch]$Force,
    [switch]$Quiet
)

$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$marker = "canvas-local-dev:"
# 老版本窗口没有标记，用"仓库路径 + 本项目注入的环境变量名"识别，避免误伤其他项目窗口。
$windowSignature = "CANVAS_BACKEND_DATA_DIR|VITE_API_PROXY_TARGET"

function Write-Step([string]$Message) { if (-not $Quiet) { Write-Host $Message -ForegroundColor Cyan } }
function Write-Detail([string]$Message) { if (-not $Quiet) { Write-Host $Message -ForegroundColor DarkGray } }
function Write-Warn([string]$Message) { Write-Host $Message -ForegroundColor Yellow }

function Test-ListeningPort([int]$Port) {
    try {
        return $null -ne (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction Stop | Select-Object -First 1)
    } catch {
        return $false
    }
}

function Get-ListeningProcessId([int]$Port) {
    try {
        return @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction Stop |
            Select-Object -ExpandProperty OwningProcess -Unique)
    } catch {
        return @()
    }
}

function Get-ProcessInfo([int]$ProcessId) {
    if (-not $ProcessId) { return $null }
    return Get-CimInstance Win32_Process -Filter "ProcessId=$ProcessId" -ErrorAction SilentlyContinue
}

# 广度优先收集整棵进程树，再反转，保证先结束叶子、最后结束启动壳，
# 避免父进程在子进程被结束的间隙重新拉起子进程。
function Get-ProcessTree([int]$ProcessId) {
    $all = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Select-Object ProcessId, ParentProcessId)
    $order = New-Object System.Collections.Generic.List[int]
    $queue = New-Object System.Collections.Generic.Queue[int]
    $queue.Enqueue($ProcessId)
    while ($queue.Count -gt 0) {
        $current = $queue.Dequeue()
        $order.Add($current)
        foreach ($child in ($all | Where-Object { [int]$_.ParentProcessId -eq $current })) {
            $queue.Enqueue([int]$child.ProcessId)
        }
    }
    $order.Reverse()
    return $order
}

function Stop-ProcessTree([int]$ProcessId, [string]$Reason, [switch]$Gentle) {
    $info = Get-ProcessInfo $ProcessId
    if (-not $info) { return $false }
    Write-Detail ("结束 {0} (PID {1})：{2}" -f $info.Name, $ProcessId, $Reason)

    if ($Gentle -and -not $Force) {
        $requested = $false
        try {
            $process = Get-Process -Id $ProcessId -ErrorAction Stop
            $requested = $process.CloseMainWindow()
        } catch {
            $requested = $false
        }
        if ($requested) {
            for ($i = 0; $i -lt 6; $i++) {
                Start-Sleep -Milliseconds 500
                if (-not (Get-Process -Id $ProcessId -ErrorAction SilentlyContinue)) { return $true }
            }
            Write-Detail "  → 窗口未在 3 秒内关闭，改为结束进程树"
        }
    }

    foreach ($id in (Get-ProcessTree $ProcessId)) {
        Stop-Process -Id $id -Force -ErrorAction SilentlyContinue
    }
    return $true
}

# 收集自身及祖先进程：绝不允许把自己所在的 shell 当成开发窗口误杀。
function Get-SelfAndAncestorIds {
    $ids = New-Object System.Collections.Generic.HashSet[int]
    $current = [int]$PID
    for ($i = 0; $i -lt 10 -and $current; $i++) {
        [void]$ids.Add($current)
        $info = Get-ProcessInfo $current
        if (-not $info) { break }
        $current = [int]$info.ParentProcessId
    }
    return $ids
}

function Test-ProjectBackendCommand([string]$CommandLine) {
    if (-not $CommandLine) { return $false }
    if ($CommandLine -match 'cmd[/\\]server') { return $true }
    if ($CommandLine -match 'yingce-server') { return $true }
    if ($CommandLine -match 'go-build' -and $CommandLine -match 'server\.exe') { return $true }
    return $false
}

function Test-ProjectFrontendCommand([string]$CommandLine) {
    if (-not $CommandLine) { return $false }
    if ($CommandLine -match 'vite') { return $true }
    if ($CommandLine -match 'bun(.exe)?"?\s+run\s+dev') { return $true }
    return $false
}

Write-Step "正在停止影策本地开发服务..."

# 1) 开发窗口（连同其子进程）
#    必须同时满足 -NoExit（本项目窗口的启动参数）与"标记 + 仓库路径 / 仓库路径 + 特征变量"，
#    并排除自身与祖先进程，避免把提到这些变量名的普通命令行误当成开发窗口。
$protectedIds = Get-SelfAndAncestorIds
$windows = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
        $_.CommandLine -and
        ($_.Name -eq "powershell.exe" -or $_.Name -eq "pwsh.exe") -and
        $_.CommandLine.Contains("-NoExit") -and
        -not $protectedIds.Contains([int]$_.ProcessId) -and
        (
            $_.CommandLine.Contains($marker) -or
            ($_.CommandLine.Contains($repoRoot) -and $_.CommandLine -match $windowSignature)
        )
    })
if ($windows.Count -eq 0) {
    Write-Detail "未发现影策开发窗口。"
} else {
    foreach ($proc in $windows) {
        $role = if ($proc.CommandLine -match "$([regex]::Escape($marker))(\w+)") { $Matches[1] } else { "老版本窗口" }
        Stop-ProcessTree -ProcessId $proc.ProcessId -Reason "开发窗口（$role）" -Gentle | Out-Null
    }
    Start-Sleep -Seconds 2
}

# 2) 剩余端口占用者：只处理看起来确实属于本项目的进程
$ports = @(
    @{ Port = $BackendPort; Label = "后端"; Pattern = ${function:Test-ProjectBackendCommand} },
    @{ Port = $WebPort; Label = "前端"; Pattern = ${function:Test-ProjectFrontendCommand} }
)

foreach ($entry in $ports) {
    if (-not (Test-ListeningPort $entry.Port)) { continue }
    foreach ($processId in (Get-ListeningProcessId $entry.Port)) {
        if ($protectedIds.Contains([int]$processId)) { continue }
        $info = Get-ProcessInfo $processId
        if (-not $info) { continue }
        $commandLine = $info.CommandLine
        if (-not (& $entry.Pattern $commandLine)) {
            Write-Warn ("端口 {0} 被非影策进程占用（{1}，PID {2}），未处理：{3}" -f $entry.Port, $info.Name, $processId, $commandLine)
            continue
        }
        Stop-ProcessTree -ProcessId $processId -Reason "$($entry.Label)进程（端口 $($entry.Port)）" | Out-Null
        # 顺链向上收掉仍存活的启动壳（go run / vite.exe / bun / 开发窗口），避免留下空壳进程
        $parentId = [int]$info.ParentProcessId
        for ($depth = 0; $depth -lt 5 -and $parentId; $depth++) {
            if ($protectedIds.Contains([int]$parentId)) { break }
            $parent = Get-ProcessInfo $parentId
            if (-not $parent) { break }
            $parentLooksOurs = ($parent.CommandLine -and ($parent.CommandLine.Contains($marker) -or (& $entry.Pattern $parent.CommandLine)))
            if (-not $parentLooksOurs) { break }
            $nextParentId = [int]$parent.ParentProcessId
            Stop-ProcessTree -ProcessId $parentId -Reason "$($entry.Label)启动壳（第 $($depth + 1) 层父进程）" | Out-Null
            $parentId = $nextParentId
        }
    }
    Start-Sleep -Seconds 2
}

# 3) 复核
$remaining = @()
foreach ($entry in $ports) {
    if (Test-ListeningPort $entry.Port) {
        $owners = (Get-ListeningProcessId $entry.Port) -join ','
        $remaining += "端口 $($entry.Port) 仍被 PID $owners 占用"
    }
}

if ($remaining.Count -gt 0) {
    Write-Warn ("停止未完全成功：" + ($remaining -join "；"))
    Write-Warn "可查看占用者后手动处理：Get-Process -Id <PID> | Select-Object Id,ProcessName,Path"
    exit 1
}

Write-Host "影策本地开发服务已停止（端口 $BackendPort / $WebPort 已释放）。" -ForegroundColor Green
exit 0
