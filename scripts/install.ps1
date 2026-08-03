$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$binary = Join-Path $projectRoot 'dist\codex-session-guard.exe'
$binDir = Join-Path $env:USERPROFILE '.local\bin'

if (-not (Test-Path -LiteralPath $binary)) {
    & (Join-Path $projectRoot 'scripts\build.ps1')
}

$originalUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$originalProcessPath = $env:PATH
$userPathChanged = $false
try {
    & $binary install --bin-dir $binDir
    if ($LASTEXITCODE -ne 0) { throw 'Codex Session Guard installation failed' }

    $entries = @($originalUserPath -split ';' | Where-Object { $_ })
    $otherEntries = @($entries | Where-Object {
        $expanded = [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\')
        -not [string]::Equals($expanded, $binDir.TrimEnd('\'), [StringComparison]::OrdinalIgnoreCase)
    })
    $newUserPath = (@($binDir) + $otherEntries) -join ';'
    if (-not [string]::Equals($newUserPath, $originalUserPath, [StringComparison]::OrdinalIgnoreCase)) {
        [Environment]::SetEnvironmentVariable('Path', $newUserPath, 'User')
        $userPathChanged = $true
    }
    $processEntries = @($env:PATH -split ';' | Where-Object {
        $_ -and -not [string]::Equals(
            [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\'),
            $binDir.TrimEnd('\'),
            [StringComparison]::OrdinalIgnoreCase)
    })
    $env:PATH = (@($binDir) + $processEntries) -join ';'

    & (Join-Path $binDir 'csg.exe') doctor
    if ($LASTEXITCODE -ne 0) { throw 'Post-install doctor check failed' }
    Write-Host 'Installed. Direct codex is not monitored; use csg run <launcher> to monitor sessions, then csg list, csg resume <session-id>, or csg delete <session-id>.'
}
catch {
    $env:PATH = $originalProcessPath
    if ($userPathChanged) {
        [Environment]::SetEnvironmentVariable('Path', $originalUserPath, 'User')
    }
    throw
}
