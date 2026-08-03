$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$binary = Join-Path $projectRoot 'dist\codex-session-guard.exe'
if (-not (Test-Path -LiteralPath $binary)) {
    & (Join-Path $projectRoot 'scripts\build.ps1')
}

$testRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('codex-session-guard-test-' + [guid]::NewGuid().ToString('N'))
$binDir = Join-Path $testRoot 'bin'
$guardHome = Join-Path $testRoot 'guard-home'
$codexHome = Join-Path $testRoot 'codex-home'
$fakeCodex = Join-Path $testRoot 'fake-codex.exe'
New-Item -ItemType Directory -Force -Path $binDir, $guardHome, $codexHome | Out-Null
Copy-Item -LiteralPath $binary -Destination $fakeCodex

$oldGuardHome = $env:CODEX_SESSION_GUARD_HOME
$oldCodexHome = $env:CODEX_HOME
$oldPath = $env:PATH
try {
    $env:CODEX_SESSION_GUARD_HOME = $guardHome
    $env:CODEX_HOME = $codexHome
    $env:PATH = "$binDir;$oldPath"

	$upstreamCodex = Join-Path $binDir 'codex.cmd'
	$upstreamContents = "@echo off`r`n`"$fakeCodex`" %*`r`nexit /b %errorlevel%`r`n"
	[System.IO.File]::WriteAllText($upstreamCodex, $upstreamContents)
	$upstreamHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $upstreamCodex).Hash

    & $binary install --bin-dir $binDir
    if ($LASTEXITCODE -ne 0) { throw 'isolated install failed' }
    Copy-Item -LiteralPath (Join-Path $binDir 'csg.exe') -Destination (Join-Path $binDir 'codex-recover.exe')
    Copy-Item -LiteralPath (Join-Path $binDir 'csg.exe') -Destination (Join-Path $binDir 'codex.exe')
    & $binary install --bin-dir $binDir
    if ($LASTEXITCODE -ne 0) { throw 'isolated reinstall failed' }
    if (Test-Path -LiteralPath (Join-Path $binDir 'codex-recover.exe')) {
        throw 'upgrade did not remove the owned legacy codex-recover command'
    }
	if (Test-Path -LiteralPath (Join-Path $binDir 'codex.exe')) {
		throw 'upgrade did not remove the owned legacy codex wrapper'
	}
	if ((Get-FileHash -Algorithm SHA256 -LiteralPath $upstreamCodex).Hash -ne $upstreamHash) {
		throw 'install or upgrade modified the upstream codex command'
	}

    $powerShellResolution = (Get-Command codex -ErrorAction Stop).Source
	if (-not [string]::Equals($powerShellResolution, $upstreamCodex, [StringComparison]::OrdinalIgnoreCase)) {
        throw "PowerShell resolved the wrong codex: $powerShellResolution"
    }
    $cmdResolution = @(where.exe codex)[0]
	if (-not [string]::Equals($cmdResolution, $upstreamCodex, [StringComparison]::OrdinalIgnoreCase)) {
        throw "CMD resolved the wrong codex: $cmdResolution"
    }

    $csgResolution = (Get-Command csg -ErrorAction Stop).Source
    if (-not [string]::Equals($csgResolution, (Join-Path $binDir 'csg.exe'), [StringComparison]::OrdinalIgnoreCase)) {
        throw "PowerShell resolved the wrong csg: $csgResolution"
    }

    & (Join-Path $binDir 'csg.exe') $fakeCodex --fake-exit=0
    if ($LASTEXITCODE -ne 2) { throw 'legacy csg <launcher> syntax was not rejected' }

	& $upstreamCodex --fake-exit=0
    if ($LASTEXITCODE -ne 0) { throw 'normal fake Codex failed' }
	& $upstreamCodex --fake-exit=7
    if ($LASTEXITCODE -ne 7) { throw 'untracked fake Codex exit code was not preserved' }
    $directRuns = @(Get-ChildItem -LiteralPath (Join-Path $guardHome 'runs') -Filter '*.json' -ErrorAction SilentlyContinue)
    if ($directRuns.Count -ne 0) { throw 'direct codex unexpectedly created a monitoring record' }

    $genericSession = 'bbbbbbbb-cccc-4ddd-8eee-ffffffffffff'
    & (Join-Path $binDir 'csg.exe') run $fakeCodex "--fake-session=$genericSession" --fake-exit=6
    if ($LASTEXITCODE -ne 6) { throw 'generic csg exit code was not preserved' }
    $genericList = & (Join-Path $binDir 'csg.exe') list | Out-String
    if ($genericList -notmatch $genericSession -or $genericList -notmatch [regex]::Escape($fakeCodex)) {
        throw 'generic csg session did not retain its original launcher'
    }
    & (Join-Path $binDir 'csg.exe') resume $genericSession
    if ($LASTEXITCODE -ne 0) { throw 'generic csg recovery failed' }
    $afterGenericRecovery = @(Get-ChildItem -LiteralPath (Join-Path $guardHome 'runs') -Filter '*.json' -ErrorAction SilentlyContinue)
    if ($afterGenericRecovery.Count -ne 0) { throw 'generic csg recovery left a record' }

    $crashSession = 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee'
    $crashProcess = Start-Process -FilePath (Join-Path $binDir 'csg.exe') -ArgumentList 'run', $fakeCodex, "--fake-session=$crashSession", '--fake-sleep-ms=1500' -PassThru -WindowStyle Hidden
    $deadline = [DateTime]::UtcNow.AddSeconds(5)
    do {
        Start-Sleep -Milliseconds 50
        $bound = Get-ChildItem -LiteralPath (Join-Path $guardHome 'runs') -Filter '*.json' -ErrorAction SilentlyContinue |
            Where-Object { (Get-Content -Raw -LiteralPath $_.FullName) -match $crashSession }
    } while (-not $bound -and [DateTime]::UtcNow -lt $deadline)
    if (-not $bound) { throw 'crash simulation never bound a session' }
    $activeList = & (Join-Path $binDir 'csg.exe') list | Out-String
    if ($activeList -notmatch 'Tracked sessions: 1 active, 0 unknown, 0 crashed' -or
        $activeList -notmatch 'RUNNING' -or
        $activeList -notmatch $crashSession) {
        throw "csg list did not report the live session as RUNNING:`n$activeList"
    }
    $crashProcess.Kill()
    $crashProcess.WaitForExit()

    $deadline = [DateTime]::UtcNow.AddSeconds(5)
    do {
        $afterGuardExit = & (Join-Path $binDir 'csg.exe') list | Out-String
        if ($afterGuardExit -match $crashSession) { break }
        Start-Sleep -Milliseconds 50
    } while ([DateTime]::UtcNow -lt $deadline)
    if ($afterGuardExit -notmatch $crashSession) { throw 'wrapper kill did not stop the Job tree and leave a recoverable session' }
    if ($afterGuardExit -notmatch 'Tracked sessions: 0 active, 0 unknown, 1 crashed' -or $afterGuardExit -notmatch 'CRASHED') {
        throw "csg list did not report the stopped session as CRASHED:`n$afterGuardExit"
    }

    & (Join-Path $binDir 'csg.exe') resume $crashSession
    if ($LASTEXITCODE -ne 0) { throw 'recover command failed' }
    $afterRecovery = @(Get-ChildItem -LiteralPath (Join-Path $guardHome 'runs') -Filter '*.json' -ErrorAction SilentlyContinue)
    if ($afterRecovery.Count -ne 0) { throw 'successful recovery did not clear the old record' }

    $beforeClearSession = '11111111-2222-4333-8444-555555555555'
    $afterClearSession = '66666666-7777-4888-8999-aaaaaaaaaaaa'
    & (Join-Path $binDir 'csg.exe') run $fakeCodex "--fake-session=$beforeClearSession" "--fake-second-session=$afterClearSession" --fake-exit=7
    if ($LASTEXITCODE -ne 7) { throw 'abnormal exit code was not preserved' }
    $abnormalRuns = @(Get-ChildItem -LiteralPath (Join-Path $guardHome 'runs') -Filter '*.json')
    if ($abnormalRuns.Count -ne 1) { throw 'abnormal exit did not leave exactly one record' }

    $list = & (Join-Path $binDir 'csg.exe') list | Out-String
    if ($list -notmatch $afterClearSession) { throw 'recover list missed the post-clear session' }
    if ($list -match $beforeClearSession) { throw 'recover list kept the pre-clear session' }
    if ($list -notmatch 'Tracked sessions: 0 active, 0 unknown, 1 crashed') { throw 'tracked session counts are wrong' }

    & (Join-Path $binDir 'csg.exe') delete $afterClearSession
    if ($LASTEXITCODE -ne 0) { throw 'delete command failed' }
    $afterDelete = & (Join-Path $binDir 'csg.exe') list | Out-String
    if ($afterDelete -match $afterClearSession) { throw 'delete command kept the session' }
    $afterDeleteRuns = @(Get-ChildItem -LiteralPath (Join-Path $guardHome 'runs') -Filter '*.json' -ErrorAction SilentlyContinue)
    if ($afterDeleteRuns.Count -ne 0) { throw 'delete command left a run record' }

    & (Join-Path $binDir 'csg.exe') doctor
    if ($LASTEXITCODE -ne 0) { throw 'doctor failed' }

    & (Join-Path $binDir 'codex-session-guard.exe') uninstall
    if ($LASTEXITCODE -ne 0) { throw 'isolated uninstall failed' }
    if (Test-Path -LiteralPath (Join-Path $codexHome 'hooks.json')) {
        throw 'tool-created hooks.json remained after uninstall'
    }
    $configAfterUninstall = Get-Content -Raw -LiteralPath (Join-Path $codexHome 'config.toml')
    if ($configAfterUninstall -match 'Codex Session Guard') {
        throw 'managed trust block remained after uninstall'
    }

    $brokenRoot = Join-Path $testRoot 'broken-install'
    $brokenBin = Join-Path $brokenRoot 'bin'
    $brokenGuardHome = Join-Path $brokenRoot 'guard-home'
    $brokenCodexHome = Join-Path $brokenRoot 'codex-home'
    New-Item -ItemType Directory -Force -Path $brokenBin, $brokenGuardHome, $brokenCodexHome | Out-Null
    Set-Content -LiteralPath (Join-Path $brokenCodexHome 'hooks.json') -Value '{ invalid json' -NoNewline
    $env:CODEX_SESSION_GUARD_HOME = $brokenGuardHome
    $env:CODEX_HOME = $brokenCodexHome
    $env:PATH = "$brokenBin;$oldPath"
	& $binary install --bin-dir $brokenBin
    if ($LASTEXITCODE -eq 0) { throw 'broken hooks.json unexpectedly installed' }
	foreach ($name in 'csg.exe', 'codex-session-guard.exe') {
        if (Test-Path -LiteralPath (Join-Path $brokenBin $name)) {
            throw "failed install left command behind: $name"
        }
    }
    if ((Get-Content -Raw -LiteralPath (Join-Path $brokenCodexHome 'hooks.json')) -ne '{ invalid json') {
        throw 'failed install did not restore hooks.json'
    }
}
finally {
    $env:CODEX_SESSION_GUARD_HOME = $oldGuardHome
    $env:CODEX_HOME = $oldCodexHome
    $env:PATH = $oldPath
    if (Test-Path -LiteralPath $testRoot) {
        Remove-Item -Recurse -Force -LiteralPath $testRoot
    }
}
Write-Host 'Integration test passed.'
exit 0
