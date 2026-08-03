$ErrorActionPreference = 'Stop'
$binDir = Join-Path $env:USERPROFILE '.local\bin'
$manager = Join-Path $binDir 'codex-session-guard.exe'
if (Test-Path -LiteralPath $manager) {
    $guardStateRoot = Join-Path $env:LOCALAPPDATA 'CodexSessionGuard'
    $settingsFile = Join-Path $guardStateRoot 'settings.json'
    if (-not (Test-Path -LiteralPath $settingsFile)) {
        throw "Cannot find $settingsFile; refusing to remove files without ownership confirmation."
    }
    $settings = Get-Content -Raw -LiteralPath $settingsFile | ConvertFrom-Json
    if (-not [string]::Equals([string]$settings.install_dir, $binDir, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'settings.json install directory does not match the target directory; uninstall stopped.'
    }
    $expectedHash = [string]$settings.binary_hash
    if ($expectedHash.StartsWith('sha256:', [StringComparison]::OrdinalIgnoreCase)) {
        $expectedHash = $expectedHash.Substring(7)
    }
    if (-not $expectedHash) {
        $expectedHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $manager).Hash
    }
    & $manager uninstall
    if ($LASTEXITCODE -ne 0) { throw 'Hook removal failed; uninstall stopped.' }
    foreach ($name in 'codex.exe', 'codex-recover.exe', 'csg.exe', 'codex-session-guard.exe') {
        $target = Join-Path $binDir $name
        if (-not (Test-Path -LiteralPath $target)) { continue }
        $targetHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $target).Hash
        if ($targetHash -eq $expectedHash) {
            Remove-Item -Force -LiteralPath $target
        }
        else {
            Write-Warning "Skipping a file that was replaced by another program: $target"
        }
    }
}
else {
    throw "Cannot find $manager; refusing to remove files without ownership confirmation."
}
Write-Host 'Codex Session Guard uninstalled. The PATH entry, recovery records, and backups were preserved.'
