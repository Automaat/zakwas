<#
.SYNOPSIS
Installs zakwas on Windows from a GitHub release.

.DESCRIPTION
Downloads the release zip for this machine's architecture, checks its SHA256
against the release's checksums.txt and copies zakwas.exe into Dir. It does
not change PATH unless -AddToPath is given. Windows support is early: see
the Windows section of the README.

.PARAMETER Version
Release to install, X.Y.Z (default: the latest release).

.PARAMETER Dir
Where to put zakwas.exe (default: %LOCALAPPDATA%\Programs\zakwas).

.PARAMETER BaseUrl
Where releases are published. Only tests override it.

.PARAMETER AddToPath
Add Dir to your user PATH.
#>
[CmdletBinding()]
param(
    [string]$Version = 'latest',
    [string]$Dir = (Join-Path $env:LOCALAPPDATA 'Programs\zakwas'),
    [string]$BaseUrl = 'https://github.com/Automaat/zakwas/releases',
    [switch]$AddToPath
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

function Write-Step([string]$Message) {
    Write-Information "==> $Message" -InformationAction Continue
}

function Get-LatestVersion([string]$Base) {
    $request = [Net.HttpWebRequest]::Create("$Base/latest")
    $request.Method = 'HEAD'
    $request.AllowAutoRedirect = $false
    $response = $request.GetResponse()
    try {
        $location = $response.Headers['Location']
    } finally {
        $response.Close()
    }
    if (-not $location) {
        throw "can't find the latest zakwas release: $Base/latest did not redirect"
    }
    return ($location -split '/')[-1].TrimStart('v')
}

function Get-Architecture {
    switch ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
        'X64' { return 'amd64' }
        'Arm64' { return 'arm64' }
        default { throw "zakwas has no Windows build for $_" }
    }
}

$Base = $BaseUrl.TrimEnd('/')
if ($Version -eq 'latest') {
    $Version = Get-LatestVersion $Base
}
$Version = $Version.TrimStart('v')
if ($Version -notmatch '^\d+\.\d+\.\d+([-+][0-9A-Za-z.+-]+)?$') {
    throw "'$Version' is not a release version (X.Y.Z)"
}

$asset = "zakwas_${Version}_windows_$(Get-Architecture).zip"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("zakwas-install-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Step "Downloading $asset"
    $release = "$Base/download/v$Version"
    Invoke-WebRequest -UseBasicParsing -Uri "$release/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')
    Invoke-WebRequest -UseBasicParsing -Uri "$release/$asset" -OutFile (Join-Path $tmp $asset)

    $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match "^[0-9a-fA-F]{64}\s+\*?$([regex]::Escape($asset))$" } | Select-Object -First 1
    if (-not $line) {
        throw "checksums.txt has no entry for $asset"
    }
    $want = ($line -split '\s+')[0].ToLowerInvariant()
    $got = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $asset)).Hash.ToLowerInvariant()
    if ($got -ne $want) {
        throw "$asset: sha256 $got, want $want"
    }

    Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath (Join-Path $tmp 'out')
    New-Item -ItemType Directory -Force -Path $Dir | Out-Null
    $target = Join-Path $Dir 'zakwas.exe'
    Copy-Item -Force -Path (Join-Path $tmp 'out\zakwas.exe') -Destination $target
    Write-Step "Installed zakwas $Version to $target"
} finally {
    Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
}

$onPath = ($env:Path -split ';') -contains $Dir
if ($AddToPath) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (($userPath -split ';') -notcontains $Dir) {
        [Environment]::SetEnvironmentVariable('Path', (($userPath, $Dir) -join ';').Trim(';'), 'User')
    }
    Write-Step 'Added the install directory to your user PATH; open a new terminal to use zakwas'
} elseif (-not $onPath) {
    Write-Step "Add $Dir to your PATH, or run the installer again with -AddToPath"
}
