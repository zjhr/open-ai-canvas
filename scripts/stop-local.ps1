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
    [int]$GatewayPort = 19000,
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

# 读不到命令行时（跨会话启动的进程会返回空），改用"问端口自己是谁"。
# 后端在 /api/openapi.yaml 里自报了标题，这是运行中进程自己的证据，
# 比按进程名猜可靠：server.exe 这种通用名字可能属于别的程序。
function Test-GatewayCommand([string]$CommandLine) {
    if (-not $CommandLine) { return $false }
    return ($CommandLine -match 'api_gateway\.py')
}

# Gateway identity fallback: /health reports its own comfy URL and model list.
function Test-GatewayIdentityByHttp([int]$Port) {
    try {
        $client = New-Object System.Net.WebClient
        $client.Headers.Add("Accept", "application/json")
        $bytes = $client.DownloadData("http://127.0.0.1:$Port/health")
        if (-not $bytes -or $bytes.Length -lt 16) { return $false }
        $text = [System.Text.Encoding]::UTF8.GetString($bytes)
        return ($text -match '"comfy"\s*:')
    } catch {
        return $false
    } finally {
        if ($client) { $client.Dispose() }
    }
}

function Test-BackendIdentityByHttp([int]$Port) {
    try {
        $client = New-Object System.Net.WebClient
        $client.Encoding = [System.Text.Encoding]::UTF8
        $client.Headers.Add("Accept", "application/yaml, text/plain")
        $bytes = $client.DownloadData("http://127.0.0.1:$Port/api/openapi.yaml")
        if (-not $bytes -or $bytes.Length -lt 32) { return $false }
        $text = [System.Text.Encoding]::UTF8.GetString($bytes)
        return ($text -match '影策\s*HTTP\s*API' -or $text -match 'openapi:\s*3\.')
    } catch {
        return $false
    } finally {
        if ($client) { $client.Dispose() }
    }
}

# 前端同理：Vite 会在 /@vite/client 暴露特征路径。
function Test-FrontendIdentityByHttp([int]$Port) {
    try {
        $client = New-Object System.Net.WebClient
        $client.Headers.Add("Accept", "*/*")
        $bytes = $client.DownloadData("http://127.0.0.1:$Port/@vite/client")
        return ($bytes -and $bytes.Length -gt 0 -and [System.Text.Encoding]::UTF8.GetString($bytes) -match 'vite')
    } catch {
        return $false
    } finally {
        if ($client) { $client.Dispose() }
    }
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
    @{ Port = $BackendPort; Label = "后端"; Pattern = ${function:Test-ProjectBackendCommand}; Identify = ${function:Test-BackendIdentityByHttp} },
    @{ Port = $WebPort; Label = "前端"; Pattern = ${function:Test-ProjectFrontendCommand}; Identify = ${function:Test-FrontendIdentityByHttp} },
    @{ Port = $GatewayPort; Label = "视频网关"; Pattern = ${function:Test-GatewayCommand}; Identify = ${function:Test-GatewayIdentityByHttp} }
)

foreach ($entry in $ports) {
    if (-not (Test-ListeningPort $entry.Port)) { continue }
    foreach ($processId in (Get-ListeningProcessId $entry.Port)) {
        if ($protectedIds.Contains([int]$processId)) { continue }
        $info = Get-ProcessInfo $processId
        if (-not $info) { continue }
        $commandLine = $info.CommandLine
        # 命令行匹配优先；读不到命令行（跨会话进程常见）时退回"问端口自己是谁"。
        $looksOurs = (& $entry.Pattern $commandLine)
        $identifiedBy = "命令行"
        if (-not $looksOurs -and [string]::IsNullOrWhiteSpace($commandLine)) {
            if (& $entry.Identify $entry.Port) {
                $looksOurs = $true
                $identifiedBy = "端口自报身份"
            }
        }
        if (-not $looksOurs) {
            Write-Warn ("端口 {0} 被非影策进程占用（{1}，PID {2}），未处理：{3}" -f $entry.Port, $info.Name, $processId, $commandLine)
            if ([string]::IsNullOrWhiteSpace($commandLine)) {
                Write-Warn ("  该进程的命令行不可读（通常是跨会话启动），已尝试按端口自报名识别但未确认是影策服务。")
            }
            continue
        }
        if ($identifiedBy -ne "命令行") { Write-Detail ("PID {0} 由{1}确认属于本项目。" -f $processId, $identifiedBy) }
        Stop-ProcessTree -ProcessId $processId -Reason "$($entry.Label)进程（端口 $($entry.Port)）" | Out-Null
        # 顺链向上收掉仍存活的启动壳（go run / vite.exe / bun / 开发窗口），避免留下空壳进程。
        # 只收"确定是启动链上的一环"的父进程：命令行读不到时不能无脑向上，
        # 否则一旦父进程是用户自己的终端窗口就会被误杀。
        $parentId = [int]$info.ParentProcessId
        for ($depth = 0; $depth -lt 3 -and $parentId; $depth++) {
            if ($protectedIds.Contains([int]$parentId)) { break }
            $parent = Get-ProcessInfo $parentId
            if (-not $parent) { break }
            # 交互式终端（explorer、Windows Terminal、ssh 会话壳）绝不在回收范围内。
            $shellNames = @("explorer.exe", "WindowsTerminal.exe", "wt.exe", "sshd.exe", "services.exe", "svchost.exe")
            if ($shellNames -contains $parent.Name) { break }
            $commandGone = [string]::IsNullOrWhiteSpace($parent.CommandLine)
            if ($commandGone) {
                # 命令行不可读时，只回收「明确属于构建/启动工具链」的进程名，
                # 不认识的一律停手——宁可留下一个空壳，也不能杀掉用户的终端。
                $launcherNames = @("go.exe", "bun.exe", "node.exe", "cmd.exe", "powershell.exe", "pwsh.exe")
                if ($shellNames -contains $parent.Name -or -not ($launcherNames -contains $parent.Name)) { break }
                # 即使名字像启动壳，也要确认它确实在我们的标记链路上：读不到就只允许收 1 层。
                if ($depth -ge 1) { break }
            } else {
                $parentLooksOurs = $parent.CommandLine.Contains($marker) -or (& $entry.Pattern $parent.CommandLine)
                if (-not $parentLooksOurs) { break }
            }
            $nextParentId = [int]$parent.ParentProcessId
            Stop-ProcessTree -ProcessId $parentId -Reason "$($entry.Label)启动壳（第 $($depth + 1) 层父进程）" | Out-Null
            $parentId = $nextParentId
        }
    }
    Start-Sleep -Milliseconds 800
}

# 3) 复核：进程退出到端口释放之间有延迟，先给几次重试再判定失败。
function Wait-PortsReleased($portEntries, [int]$Attempts = 6, [int]$IntervalMs = 500) {
    for ($attempt = 0; $attempt -lt $Attempts; $attempt++) {
        $stillBound = @($portEntries | Where-Object { Test-ListeningPort $_.Port })
        if ($stillBound.Count -eq 0) { return }
        Start-Sleep -Milliseconds $IntervalMs
    }
}
Wait-PortsReleased $ports

$remaining = @()
foreach ($entry in $ports) {
    if (Test-ListeningPort $entry.Port) {
        $owners = (Get-ListeningProcessId $entry.Port) -join ','
        $remaining += "端口 $($entry.Port) 仍被 PID $owners 占用"
    }
}

if ($remaining.Count -gt 0) {
    Write-Warn ("停止未完全成功：" + ($remaining -join "；"))
    # 区分「进程还活着但结束不了」与「认不出是谁」。前者通常是权限问题，
    # 给出可执行的处置方式；后者要求用户自己判断，不能建议他直接杀。
    foreach ($entry in $ports) {
        if (-not (Test-ListeningPort $entry.Port)) { continue }
        foreach ($processId in (Get-ListeningProcessId $entry.Port)) {
            $info = Get-ProcessInfo $processId
            if (-not $info) { continue }
            $alive = $null -ne (Get-Process -Id $processId -ErrorAction SilentlyContinue)
            if (-not $alive) { continue }
            $sessionNote = ""
            try {
                $self = Get-CimInstance Win32_Process -Filter "ProcessId=$PID" -ErrorAction SilentlyContinue
                if ($info.SessionId -ne $null -and $self.SessionId -ne $null -and $info.SessionId -ne $self.SessionId) {
                    $sessionNote = "（该进程在会话 $($info.SessionId)，当前脚本在会话 $($self.SessionId)）"
                }
            } catch { }
            Write-Warn ("  PID " + $processId + "（" + $info.Name + "）仍存活" + $sessionNote + "：当前权限无法结束它。")
            $hint = "Stop-Process -Id " + $processId + " -Force"
            if ($sessionNote) {
                Write-Warn ("    请以管理员或该进程所属会话的身份运行： " + $hint)
            } else {
                Write-Warn ("    请以同一用户运行： " + $hint)
            }
            Write-Warn ('    或查看来源： Get-CimInstance Win32_Process -Filter "ProcessId=' + $processId + '" | Select-Object ProcessId,Name,SessionId,ParentProcessId')
        }
    }
    exit 1
}

Write-Host "影策本地开发服务已停止（端口 $BackendPort / $WebPort / $GatewayPort 已释放）。" -ForegroundColor Green
exit 0
