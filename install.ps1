<#
.SYNOPSIS
Installs zakwas on Windows from a GitHub release.

.DESCRIPTION
Downloads the release zip for this machine's architecture, checks its SHA256
against the release's checksums.txt and copies zakwas.exe into Dir. When gh is
installed and logged in, it also verifies the zip's build attestation and
stops if that fails; without gh it says so and goes on. It does not change
PATH unless -AddToPath is given. Windows support is early: see the Windows
section of the README.

.PARAMETER Version
Release to install, X.Y.Z (default: the latest release).

.PARAMETER Dir
Where to put zakwas.exe (default: %LOCALAPPDATA%\Programs\zakwas).

.PARAMETER BaseUrl
Where releases are published. Only tests and mirrors override it. It must be
https, or http on localhost or 127.0.0.1, and may not contain credentials.

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
    $arch = $null
    try {
        $arch = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    } catch {
        $arch = $null
    }
    if (-not $arch) {
        $arch = $env:PROCESSOR_ARCHITEW6432
        if (-not $arch) {
            $arch = $env:PROCESSOR_ARCHITECTURE
        }
    }
    switch ($arch) {
        { $_ -in 'X64', 'AMD64' } { return 'amd64' }
        { $_ -in 'Arm64', 'ARM64' } { return 'arm64' }
        default { throw "zakwas has no Windows build for '$arch'" }
    }
}

function Assert-BaseUrl([string]$Url) {
    $uri = $null
    if (-not [Uri]::TryCreate($Url, [UriKind]::Absolute, [ref]$uri)) {
        throw "-BaseUrl '$Url' is not an absolute URL"
    }
    if ($Url.Contains('@')) {
        throw '-BaseUrl must not contain credentials'
    }
    $local = $uri.Host -in 'localhost', '127.0.0.1'
    if ($uri.Scheme -eq 'http' -and -not $local) {
        throw '-BaseUrl must be https, or http on localhost or 127.0.0.1'
    }
    if ($uri.Scheme -notin 'http', 'https') {
        throw "-BaseUrl scheme '$($uri.Scheme)' is not allowed: use https"
    }
}

function Save-Download([string]$Url, [string]$OutFile) {
    $response = Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $OutFile -PassThru
    $final = $response.BaseResponse.ResponseUri
    if (-not $final -and $response.BaseResponse.RequestMessage) {
        $final = $response.BaseResponse.RequestMessage.RequestUri
    }
    if ($Url.StartsWith('https:') -and $final -and $final.Scheme -ne 'https') {
        Remove-Item -Force -Path $OutFile -ErrorAction SilentlyContinue
        throw "$Url redirected to $final, which is not https"
    }
}

# The first release published with build provenance attestations.
$AttestedSince = '0.4.0'

function Test-VersionAtLeast([string]$Have, [string]$Want) {
    $a = @(($Have -split '[-+]')[0] -split '\.')
    $b = @(($Want -split '[-+]')[0] -split '\.')
    for ($i = 0; $i -lt 3; $i++) {
        if ([int]$a[$i] -gt [int]$b[$i]) { return $true }
        if ([int]$a[$i] -lt [int]$b[$i]) { return $false }
    }
    return $true
}

# gh must be installed, logged in, and new enough to know --source-ref.
function Test-GhCanVerify {
    # Windows PowerShell 5.1 turns a native command's stderr into an error
    # that Stop would throw.
    $ErrorActionPreference = 'Continue'
    if (-not (Get-Command -Name gh -CommandType Application -ErrorAction SilentlyContinue)) {
        return $false
    }
    & gh auth status *> $null
    if ($LASTEXITCODE -ne 0) {
        return $false
    }
    $help = (& gh attestation verify --help 2>&1 | Out-String)
    return ($LASTEXITCODE -eq 0 -and $help.Contains('--source-ref'))
}

function Assert-Attestation([string]$File, [string]$Release) {
    $ErrorActionPreference = 'Continue'
    $name = Split-Path -Leaf $File
    if (-not (Test-VersionAtLeast $Release $AttestedSince)) {
        Write-Step "zakwas $Release predates build attestations; skipping provenance check"
        return
    }
    $options = @('--repo', 'Automaat/zakwas', '--signer-workflow', 'Automaat/zakwas/.github/workflows/release.yml', '--source-ref', "refs/tags/v$Release")
    if (Test-GhCanVerify) {
        Write-Step "Verifying build provenance of $name"
        & gh attestation verify $File @options
        if ($LASTEXITCODE -ne 0) {
            throw "attestation verification failed for $name"
        }
    } else {
        Write-Step "Skipped attestation check (needs a recent gh, logged in); to verify: gh release download v$Release -R Automaat/zakwas -p $name; gh attestation verify $name $($options -join ' ')"
    }
}

function Get-PathEntry([string]$Path) {
    return @($Path -split ';' | ForEach-Object { $_.Trim().TrimEnd('\') } | Where-Object { $_ })
}

Assert-BaseUrl $BaseUrl
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
    Save-Download "$release/checksums.txt" (Join-Path $tmp 'checksums.txt')
    Save-Download "$release/$asset" (Join-Path $tmp $asset)

    $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match "^[0-9a-fA-F]{64}\s+\*?$([regex]::Escape($asset))$" } | Select-Object -First 1
    if (-not $line) {
        throw "checksums.txt has no entry for $asset"
    }
    $want = ($line -split '\s+')[0].ToLowerInvariant()
    $got = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $asset)).Hash.ToLowerInvariant()
    if ($got -ne $want) {
        throw "${asset}: sha256 $got, want $want"
    }

    Assert-Attestation (Join-Path $tmp $asset) $Version

    Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath (Join-Path $tmp 'out')
    New-Item -ItemType Directory -Force -Path $Dir | Out-Null
    $target = Join-Path $Dir 'zakwas.exe'
    $staged = Join-Path $Dir ("zakwas.exe.new-" + [Guid]::NewGuid().ToString('N'))
    try {
        Copy-Item -Force -Path (Join-Path $tmp 'out\zakwas.exe') -Destination $staged
        Move-Item -Force -Path $staged -Destination $target
    } catch {
        throw "can't replace $target (is zakwas running? close it and run the installer again): $($_.Exception.Message)"
    } finally {
        Remove-Item -Force -Path $staged -ErrorAction SilentlyContinue
    }
    Write-Step "Installed zakwas $Version to $target"
} finally {
    Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
}

$wanted = $Dir.Trim().TrimEnd('\')
$onPath = (Get-PathEntry $env:Path) -contains $wanted
if ($AddToPath) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
    try {
        $raw = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        $kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
        if ($key.GetValueNames() -contains 'Path') {
            $kind = $key.GetValueKind('Path')
        }
        $entries = @($raw -split ';' | Where-Object { $_.Trim() })
        if (-not ((Get-PathEntry $raw) -contains $wanted)) {
            $key.SetValue('Path', (($entries + $Dir) -join ';'), $kind)
        }
    } finally {
        $key.Close()
    }
    [Environment]::SetEnvironmentVariable('ZAKWAS_PATH_REFRESH', $null, 'User')
    Write-Step 'Added the install directory to your user PATH; open a new terminal to use zakwas'
} elseif (-not $onPath) {
    Write-Step "Add $Dir to your PATH, or run the installer again with -AddToPath"
}
