Set-StrictMode -Version Latest

$script:ReleaseRepository = 'atikansari-ghr/BookmarksSync'
$script:RcloneVersion = '1.76.0'
$script:RcloneURL = 'https://github.com/rclone/rclone/releases/download/v1.76.0/rclone-v1.76.0-windows-amd64.zip'
$script:RcloneSHA256 = '8b4fd2737ade4e70c057ef4a49deb7cffe94b0fb9d7587c189d149823f21a1dc'

function Assert-ApprovedTag([string]$Tag) {
    if ($Tag -notmatch '^build-[0-9a-f]{40}$') { throw 'A full published build-<SHA> tag is required.' }
}

function Assert-NoReparsePath([string]$Path) {
    $current = [IO.Path]::GetFullPath($Path)
    while ($current) {
        if (Test-Path -LiteralPath $current) {
            $item = Get-Item -LiteralPath $current -Force
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Install paths cannot contain junctions or symbolic links.' }
        }
        $parent = Split-Path -Parent $current
        if (-not $parent -or $parent -eq $current) { break }
        $current = $parent
    }
}

function Assert-NoReparseTree([string]$Path) {
    Assert-NoReparsePath $Path
    if (Test-Path -LiteralPath $Path -PathType Container) {
        foreach ($item in Get-ChildItem -LiteralPath $Path -Force -Recurse) {
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Install data cannot contain junctions or symbolic links.' }
        }
    }
}

function Get-RequiredAssets {
    return @('bookmarksync-windows-amd64.exe','bookmarksync-updater-windows-amd64.exe','geoipupdate-windows-amd64.exe','GEOIPUPDATE-LICENSES.txt','BUILD_INFO.txt','SHA256SUMS')
}

function Set-GeoLiteDatabasePath([string]$EnvFile,[string]$DataDir) {
    if (-not (Test-Path -LiteralPath $EnvFile -PathType Leaf)) { throw 'The managed service environment file is missing.' }
    $dbPath=Join-Path (Split-Path -Parent $DataDir) 'geolite\database\GeoLite2-Country.mmdb'
    $lines=@(Get-Content -LiteralPath $EnvFile | Where-Object { $_ -notmatch '^BOOKMARKSYNC_GEOIP_DB=' })
    $lines += "BOOKMARKSYNC_GEOIP_DB=$dbPath"
    $temp=$EnvFile+'.geolite.tmp'
    [IO.File]::WriteAllLines($temp,$lines,[Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath $temp -Destination $EnvFile -Force
}

function Get-LatestReleaseTag([string]$GhPath) {
    $json = & $GhPath api ('repos/' + $script:ReleaseRepository + '/releases?per_page=100') 2>$null
    if ($LASTEXITCODE -ne 0) { throw 'The published release list is unavailable.' }
    $releases = ($json -join "`n") | ConvertFrom-Json
    $required = Get-RequiredAssets
    $eligible = @()
    foreach ($release in $releases) {
        $assets = @($release.assets | ForEach-Object { $_.name })
        $complete = $true
        foreach ($asset in $required) { if ($asset -notin $assets) { $complete = $false; break } }
        if ($release.tag_name -match '^build-[0-9a-f]{40}$' -and -not $release.draft -and $release.prerelease -and $release.published_at -and $complete) { $eligible += $release }
    }
    $eligible = @($eligible | Sort-Object published_at -Descending)
    if ($eligible.Count -eq 0) { throw 'No complete Windows release is available.' }
    return $eligible[0].tag_name
}

function Get-VerifiedRelease([string]$Tag, [string]$Stage, [string]$GhPath) {
    Assert-ApprovedTag $Tag
    $json = & $GhPath release view $Tag --repo $script:ReleaseRepository --json tagName,isDraft,isPrerelease,assets 2>$null
    if ($LASTEXITCODE -ne 0) { throw 'The selected release could not be verified.' }
    $release = ($json -join "`n") | ConvertFrom-Json
    if ($release.tagName -cne $Tag -or $release.isDraft -or -not $release.isPrerelease) { throw 'The selected release is not a published prerelease.' }
    $names = @($release.assets | ForEach-Object { $_.name })
    if (($names | Select-Object -Unique).Count -ne $names.Count -or @($release.assets | Where-Object { $_.size -lt 1 -or $_.size -gt 536870912 }).Count -gt 0) { throw 'Release asset metadata is invalid.' }
    foreach ($asset in Get-RequiredAssets) { if ($asset -notin $names) { throw 'The selected release is missing required Windows assets.' } }
    New-Item -ItemType Directory -Force -Path $Stage | Out-Null
    foreach ($asset in Get-RequiredAssets) {
        & $GhPath release download $Tag --repo $script:ReleaseRepository --dir $Stage --pattern $asset 2>$null
        if ($LASTEXITCODE -ne 0) { throw 'Release download failed.' }
    }
    $info = @{}
    foreach ($line in Get-Content -LiteralPath (Join-Path $Stage 'BUILD_INFO.txt')) { $key,$value = $line -split '=',2; if (-not $key -or -not $value -or $info.ContainsKey($key)) { throw 'Release build metadata is invalid.' }; $info[$key]=$value }
    $buildTime=[DateTimeOffset]::MinValue
    if ($info.Count -ne 3 -or $info.commit -cne $Tag.Substring(6) -or $info.version -cne ('0.0.0-'+$Tag.Substring(6,12)) -or -not [DateTimeOffset]::TryParse($info.build_date,[ref]$buildTime)) { throw 'Release build metadata does not match the selected tag.' }
    $sumLines = @(Get-Content -LiteralPath (Join-Path $Stage 'SHA256SUMS'))
    $sumNames = @($sumLines | ForEach-Object { if ($_ -match '^([0-9a-f]{64})  ([^/\\]+)$') { $Matches[2] } else { '' } })
    $expectedNames = @('bookmarksync-linux-amd64','bookmarksync-windows-amd64.exe','bookmarksync-updater-windows-amd64.exe','geoipupdate-windows-amd64.exe','GEOIPUPDATE-LICENSES.txt','BUILD_INFO.txt')
    if ($sumLines.Count -ne 6 -or ((($sumNames | Sort-Object) -join ',') -cne (($expectedNames | Sort-Object) -join ','))) { throw 'Release checksum manifest has an unexpected asset set.' }
    foreach ($asset in Get-RequiredAssets | Where-Object { $_ -ne 'SHA256SUMS' }) {
        $line = (Get-Content -LiteralPath (Join-Path $Stage 'SHA256SUMS') | Where-Object { $_ -match ('^[0-9a-f]{64}  ' + [regex]::Escape($asset) + '$') }) | Select-Object -First 1
        if (-not $line) { throw 'Release checksum manifest is incomplete.' }
        $expected = $line.Substring(0,64)
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $Stage $asset)).Hash.ToLowerInvariant() -cne $expected) { throw 'Release checksum verification failed.' }
    }
    foreach ($asset in Get-RequiredAssets | Where-Object { $_ -ne 'SHA256SUMS' }) {
        & $GhPath attestation verify (Join-Path $Stage $asset) --repo $script:ReleaseRepository --signer-workflow "$script:ReleaseRepository/.github/workflows/ci.yml" --source-ref refs/heads/main --source-digest $Tag.Substring(6) --predicate-type https://slsa.dev/provenance/v1 2>$null | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'GitHub build attestation verification failed.' }
    }
    return $Stage
}

function Get-AppPathAccessPolicies([System.Collections.IDictionary]$Paths) {
    $policies = @(
        [pscustomobject]@{Path=$Paths.root;ServiceRights='X';ServiceInheritance=$false;Recursive=$false}
        [pscustomobject]@{Path=$Paths.data;ServiceRights='F';ServiceInheritance=$true;Recursive=$true}
        [pscustomobject]@{Path=$Paths.env;ServiceRights='R';ServiceInheritance=$false;Recursive=$false}
        [pscustomobject]@{Path=$Paths.rclone;ServiceRights='F';ServiceInheritance=$true;Recursive=$true}
        [pscustomobject]@{Path=$Paths.snapshots;ServiceRights=$null;ServiceInheritance=$false;Recursive=$true}
        [pscustomobject]@{Path=$Paths.staging;ServiceRights=$null;ServiceInheritance=$false;Recursive=$true}
        [pscustomobject]@{Path=$Paths.geoLite;ServiceRights='X';ServiceInheritance=$false;Recursive=$false}
        [pscustomobject]@{Path=(Join-Path $Paths.geoLite 'config');ServiceRights=$null;ServiceInheritance=$false;Recursive=$true}
        [pscustomobject]@{Path=(Join-Path $Paths.geoLite 'stage');ServiceRights=$null;ServiceInheritance=$false;Recursive=$true}
        [pscustomobject]@{Path=(Join-Path $Paths.geoLite 'backup');ServiceRights=$null;ServiceInheritance=$false;Recursive=$true}
        [pscustomobject]@{Path=(Join-Path $Paths.geoLite 'database');ServiceRights='X';ServiceInheritance=$false;Recursive=$false}
        [pscustomobject]@{Path=(Join-Path $Paths.geoLite 'database\GeoLite2-Country.mmdb');ServiceRights='R';ServiceInheritance=$false;Recursive=$false}
    )
    foreach ($policy in $policies) { $policy | Add-Member -NotePropertyName Owner -NotePropertyValue 'SYSTEM' }
    return $policies
}

function Set-PrivateAcl([string]$Path, [string[]]$Principals, [bool]$Recursive = $true, [string]$Owner = 'SYSTEM') {
    $walk = @(); if ($Recursive) { $walk = @('/T','/C') }
    & icacls.exe $Path /setowner $Owner @walk | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not set SYSTEM ownership on a managed path.' }
    & icacls.exe $Path /remove 'NT SERVICE\BookmarksSync' @walk | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not remove inherited application access from a protected path.' }
    $grant = @('SYSTEM:(OI)(CI)F','BUILTIN\Administrators:(OI)(CI)F') + $Principals
    & icacls.exe $Path /inheritance:r /grant:r @grant @walk | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not apply the required Windows ACL.' }
}

function Set-BookmarksSyncPathAcls([System.Collections.IDictionary]$Paths) {
    Assert-NoReparsePath $Paths.geoLite
    if (Test-Path -LiteralPath $Paths.geoLite) { Assert-NoReparseTree $Paths.geoLite }
    New-Item -ItemType Directory -Force -Path $Paths.geoLite,(Join-Path $Paths.geoLite 'database') | Out-Null
    # Scrub old recursive grants first, then grant only traversal and active-file read access.
    Set-PrivateAcl $Paths.geoLite @() $true
    foreach ($policy in Get-AppPathAccessPolicies $Paths) {
        if (-not (Test-Path -LiteralPath $policy.Path)) { continue }
        Assert-NoReparsePath $policy.Path
        if ($policy.Recursive) { Assert-NoReparseTree $policy.Path }
        $principals = @()
        if ($policy.ServiceRights) {
            $inheritance = if ($policy.ServiceInheritance) { '(OI)(CI)' } else { '' }
            $principals = @('NT SERVICE\BookmarksSync:' + $inheritance + '(' + $policy.ServiceRights + ')')
        }
        Set-PrivateAcl $policy.Path $principals $policy.Recursive $policy.Owner
    }
}

function Set-InstallAcl([string]$Path) {
    & icacls.exe $Path /inheritance:r /grant:r 'SYSTEM:(OI)(CI)F' 'BUILTIN\Administrators:(OI)(CI)F' 'BUILTIN\Users:(OI)(CI)RX' 'NT SERVICE\BookmarksSync:(OI)(CI)RX' /T /C | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not protect the installed program files.' }
}

function Assert-CidrList([string]$Value) {
    foreach ($item in ($Value -split ',')) {
        $parts = $item.Trim() -split '/',2
        $address = $null
        if ($parts.Count -ne 2 -or -not [Net.IPAddress]::TryParse($parts[0],[ref]$address)) { throw 'Management CIDRs must be comma-separated network/prefix values.' }
        $prefix = 0
        if (-not [int]::TryParse($parts[1],[ref]$prefix) -or $prefix -lt 0 -or $prefix -gt $(if ($address.AddressFamily -eq [Net.Sockets.AddressFamily]::InterNetwork) { 32 } else { 128 })) { throw 'Management CIDR prefix is outside its address range.' }
    }
}

function Invoke-Sc([string[]]$Arguments) {
    & sc.exe @Arguments | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Windows Service Control Manager operation failed.' }
}

function Install-Services([string]$InstallDir, [string]$DataDir, [string]$EnvFile, [bool]$Repair) {
    $app = Join-Path $InstallDir 'bookmarksync.exe'
    $updater = Join-Path $InstallDir 'bookmarksync-updater.exe'
    $appPath = '"{0}" service run --env-file "{1}"' -f $app,$EnvFile
    $updaterPath = '"{0}" service run' -f $updater
    foreach ($item in @(@('BookmarksSync',$appPath,'NT SERVICE\BookmarksSync'),@('BookmarksSyncUpdater',$updaterPath,'LocalSystem'))) {
        $existing = Get-Service -Name $item[0] -ErrorAction SilentlyContinue
        if ($existing -and -not $Repair) { throw "A pre-existing $($item[0]) service prevents a fresh install." }
        if ($existing) { Invoke-Sc @('config',$item[0],"binPath= $($item[1])","obj= $($item[2])",'start= auto') }
        else { Invoke-Sc @('create',$item[0],"binPath= $($item[1])",'start= auto',"obj= $($item[2])") }
    }
    Invoke-Sc @('sidtype','BookmarksSync','unrestricted')
    Invoke-Sc @('config','BookmarksSync','depend=','BookmarksSyncUpdater')
    Invoke-Sc @('failure','BookmarksSync','reset=','86400','actions=','restart/5000/restart/15000/restart/60000')
    Invoke-Sc @('failure','BookmarksSyncUpdater','reset=','86400','actions=','restart/5000/restart/15000/restart/60000')
    if (-not [Diagnostics.EventLog]::SourceExists('BookmarksSyncUpdater')) { [Diagnostics.EventLog]::CreateEventSource('BookmarksSyncUpdater','Application') }
}

function Test-ManagedServices([string]$InstallDir, [bool]$Repair) {
    foreach ($name in @('BookmarksSync','BookmarksSyncUpdater')) {
        $service = Get-CimInstance Win32_Service -Filter "Name='$name'" -ErrorAction SilentlyContinue
        if (-not $service) { continue }
        if ($Repair -eq $false) { throw "A pre-existing $name service prevents a fresh install." }
        $expected = Join-Path $InstallDir $(if ($name -eq 'BookmarksSync') { 'bookmarksync.exe' } else { 'bookmarksync-updater.exe' })
        if (-not $service.PathName.StartsWith(('"' + $expected + '"'),[StringComparison]::OrdinalIgnoreCase)) { throw "The existing $name service is not managed by this installation." }
    }
}

function Remove-FreshInstall([System.Collections.IDictionary]$Paths) {
    foreach ($name in @('BookmarksSync','BookmarksSyncUpdater')) {
        $service = Get-CimInstance Win32_Service -Filter "Name='$name'" -ErrorAction SilentlyContinue
        if (-not $service) { continue }
        $expected = Join-Path $Paths.install $(if ($name -eq 'BookmarksSync') { 'bookmarksync.exe' } else { 'bookmarksync-updater.exe' })
        if (-not $service.PathName.StartsWith(('"' + $expected + '"'),[StringComparison]::OrdinalIgnoreCase)) { throw "Refusing to remove unmanaged service $name during install recovery." }
        Stop-Service -Name $name -Force -ErrorAction SilentlyContinue
        Invoke-Sc @('delete',$name)
    }
    Assert-NoReparsePath $Paths.root
    Assert-NoReparsePath $Paths.install
    if (Test-Path $Paths.root) { Remove-Item -LiteralPath $Paths.root -Recurse -Force }
    if (Test-Path $Paths.install) { Remove-Item -LiteralPath $Paths.install -Recurse -Force }
    if ([Diagnostics.EventLog]::SourceExists('BookmarksSyncUpdater')) { [Diagnostics.EventLog]::DeleteEventSource('BookmarksSyncUpdater') }
}

function Invoke-BookmarksSyncInstall {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][ValidateSet('install','upgrade','repair')][string]$Action,
        [string]$Tag,
        [string]$PublicUrl,
        [string]$ManagementCidrs,
        [string]$Root = "$env:ProgramData\BookmarksSync",
        [string]$InstallDir = "$env:ProgramFiles\BookmarksSync",
        [hashtable]$Operations
    )
    if ($Action -ne 'install') { Assert-ApprovedTag $Tag }
    if ($PublicUrl) { $uri=$null; if (-not [Uri]::TryCreate($PublicUrl,[UriKind]::Absolute,[ref]$uri) -or $uri.Scheme -cne 'https' -or $uri.UserInfo -or $uri.Query -or $uri.Fragment -or $uri.AbsolutePath -ne '/') { throw 'Public URL must be an HTTPS origin without a path.' } }
    if ($ManagementCidrs) { Assert-CidrList $ManagementCidrs }
    if (-not $Operations) { $Operations = New-DefaultOperations }
    $paths = [ordered]@{ root=$Root; install=$InstallDir; data=(Join-Path $Root 'data'); env=(Join-Path $Root 'bookmarksync.env'); rclone=(Join-Path $Root 'rclone'); snapshots=(Join-Path $Root 'snapshots'); staging=(Join-Path $Root 'staging'); geoLite=(Join-Path $Root 'geolite') }
    $rootFull=[IO.Path]::GetFullPath($paths.root).TrimEnd('\')
    $installFull=[IO.Path]::GetFullPath($paths.install).TrimEnd('\')
    $separator=[IO.Path]::DirectorySeparatorChar
    if ($rootFull -notmatch '^[A-Za-z]:\\' -or $installFull -notmatch '^[A-Za-z]:\\' -or $rootFull -eq [IO.Path]::GetPathRoot($rootFull).TrimEnd('\') -or $installFull -eq [IO.Path]::GetPathRoot($installFull).TrimEnd('\') -or $rootFull.Equals($installFull,[StringComparison]::OrdinalIgnoreCase) -or $rootFull.StartsWith($installFull+$separator,[StringComparison]::OrdinalIgnoreCase) -or $installFull.StartsWith($rootFull+$separator,[StringComparison]::OrdinalIgnoreCase)) { throw 'Installation paths must be separate local absolute directories below their drive roots.' }
    Assert-NoReparsePath $paths.root
    Assert-NoReparsePath $paths.install
    $fresh = $Action -eq 'install'
    if ($fresh -and ((Test-Path $paths.install) -or (Test-Path $paths.root))) { throw 'Installation data or directory already exists; use repair or upgrade after checking it.' }
    if (-not $fresh -and -not (Test-Path (Join-Path $paths.install 'bookmarksync.exe'))) { throw 'No managed installation exists to upgrade or repair.' }
    & $Operations.ValidateServices $paths.install (-not $fresh)
    $gh = & $Operations.FindGitHubCli
    if (-not $gh) { throw 'Install GitHub CLI from its official source before changing the host, then retry.' }
    & $Operations.ValidateGitHubCli $gh
    if ($fresh -and -not $Tag) { $Tag = & $Operations.SelectLatestRelease $gh; Assert-ApprovedTag $Tag }
    $stage = Join-Path ([IO.Path]::GetTempPath()) ('bookmarksync-release-' + [guid]::NewGuid().ToString('N'))
    $verified = & $Operations.VerifyRelease $Tag $stage $gh
    $rcloneStage = Join-Path ([IO.Path]::GetTempPath()) ('bookmarksync-rclone-' + [guid]::NewGuid().ToString('N'))
    try {
        & $Operations.PrepareRclone $rcloneStage
        if ($fresh) {
            New-Item -ItemType Directory -Force -Path $paths.root,$paths.data,$paths.rclone,$paths.snapshots,$paths.staging,$paths.install | Out-Null
            New-Item -ItemType File -Force -Path (Join-Path $paths.rclone 'rclone.conf') | Out-Null
            $cidrs = '127.0.0.1/32,::1/128'
            if ($ManagementCidrs) { $cidrs += ',' + $ManagementCidrs }
            $lines = @('BOOKMARKSYNC_LISTEN=0.0.0.0:18080',"BOOKMARKSYNC_DATA_DIR=$($paths.data)","BOOKMARKSYNC_MANAGEMENT_CIDRS=$cidrs")
            $lines += "BOOKMARKSYNC_RCLONE_BINARY=$(Join-Path $paths.install 'rclone.exe')"
            $lines += "BOOKMARKSYNC_RCLONE_CONFIG=$(Join-Path $paths.rclone 'rclone.conf')"
            if ($PublicUrl) { $lines += "BOOKMARKSYNC_PUBLIC_URL=$PublicUrl" }
            [IO.File]::WriteAllLines($paths.env,$lines,[Text.UTF8Encoding]::new($false))
        } elseif ($Action -ne 'upgrade') { & $Operations.ProtectPaths $paths }
        if (-not $fresh) { $snapshot = $null }
        try {
            if (-not $fresh) { & $Operations.StopServices; $snapshot = & $Operations.CreateSnapshot $paths $Tag }
            & $Operations.InstallAssets $verified $rcloneStage $paths
            Set-GeoLiteDatabasePath $paths.env $paths.data
            & $Operations.ConfigureServices $paths.install $paths.data $paths.env ($Action -eq 'repair')
            & $Operations.ProtectPaths $paths
            & $Operations.ProtectInstall $paths.install
            & $Operations.StartServices
            & $Operations.WaitReady 'http://127.0.0.1:18080/readyz'
            if (-not $fresh) { try { & $Operations.PruneSnapshots $paths } catch { Write-Warning 'The update succeeded; old snapshot cleanup will need administrator attention.' } }
        } catch {
            if (-not $fresh -and $snapshot) { & $Operations.StopServices; & $Operations.RestoreSnapshot $snapshot $paths; & $Operations.StartServices; & $Operations.WaitReady 'http://127.0.0.1:18080/readyz' }
            elseif (-not $fresh) { & $Operations.StartServices }
            else { & $Operations.RollbackFresh $paths }
            throw
        }
        if ($fresh) {
            $username = Read-Host 'Initial administrator username'
            $password = Read-Host 'Initial administrator password (minimum 12 characters)' -AsSecureString
            $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($password)
            try { $plain = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr); & $Operations.CreateAdmin $paths $username $plain }
            finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr); $plain = $null }
        }
    } finally {
        & $Operations.CleanupStage $stage
        & $Operations.CleanupStage $rcloneStage
    }
}

function New-DefaultOperations {
    return @{
        FindGitHubCli = { (Get-Command gh.exe -ErrorAction SilentlyContinue).Source }
        ValidateGitHubCli = { param($gh) if ([IO.Path]::GetFullPath($gh) -ine [IO.Path]::GetFullPath((Join-Path $env:ProgramFiles 'GitHub CLI\gh.exe'))) { throw 'Install GitHub CLI in its standard Program Files location so both Windows services can use the same verified client.' } }
        SelectLatestRelease = { param($gh) Get-LatestReleaseTag $gh }
        ValidateServices = { param($install,$repair) Test-ManagedServices $install $repair }
        RollbackFresh = { param($paths) Remove-FreshInstall $paths }
        VerifyRelease = { param($tag,$stage,$gh) Get-VerifiedRelease $tag $stage $gh }
        PrepareRclone = { param($dest) New-Item -ItemType Directory -Force $dest | Out-Null; $zip=Join-Path $dest 'rclone.zip'; Invoke-WebRequest -UseBasicParsing -Uri $script:RcloneURL -OutFile $zip; if ((Get-FileHash $zip -Algorithm SHA256).Hash.ToLowerInvariant() -cne $script:RcloneSHA256) { throw 'Pinned rclone checksum verification failed.' }; Expand-Archive -LiteralPath $zip -DestinationPath $dest; $bin=Get-ChildItem $dest -Filter rclone.exe -Recurse | Select-Object -First 1; if (-not $bin) { throw 'Pinned rclone archive is incomplete.' }; Copy-Item $bin.FullName (Join-Path $dest 'rclone.exe'); Remove-Item $zip }
        ProtectPaths = { param($p) Set-BookmarksSyncPathAcls $p }
        ProtectInstall = { param($path) Set-InstallAcl $path }
        CreateSnapshot = { param($p,$tag) Assert-NoReparseTree $p.data; New-Item -ItemType Directory -Force $p.snapshots,$p.staging | Out-Null; $stage=Join-Path $p.staging ('snapshot-source-' + [guid]::NewGuid().ToString('N')); New-Item -ItemType Directory -Force $stage | Out-Null; Copy-Item $p.data (Join-Path $stage 'data') -Recurse; Copy-Item $p.env (Join-Path $stage 'bookmarksync.env'); if (Test-Path (Join-Path $p.rclone 'rclone.conf')) { New-Item -ItemType Directory -Force (Join-Path $stage 'rclone') | Out-Null; Copy-Item (Join-Path $p.rclone 'rclone.conf') (Join-Path $stage 'rclone\rclone.conf') }; Copy-Item $p.install (Join-Path $stage 'install') -Recurse; $entries=@(Get-ChildItem -LiteralPath $stage -File -Recurse | ForEach-Object { [ordered]@{ path=$_.FullName.Substring($stage.Length+1).Replace('\','/'); sha256=(Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant(); size=$_.Length } }); [ordered]@{tag=$tag;createdAt=[DateTimeOffset]::UtcNow.ToString('o');files=$entries} | ConvertTo-Json -Depth 5 | Set-Content -Encoding utf8 (Join-Path $stage 'manifest.json'); $archive=Join-Path $p.snapshots ("snapshot-{0}-{1}.zip" -f (Get-Date -Format 'yyyyMMddTHHmmssfffZ'),$tag.Substring(6,12)); Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $archive -CompressionLevel Optimal -ErrorAction Stop; $hash=(Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant(); Set-Content -NoNewline -Encoding ascii ($archive+'.sha256') $hash; if ((Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant() -cne (Get-Content -Raw ($archive+'.sha256'))) { throw 'Snapshot verification failed.' }; Remove-Item $stage -Recurse -Force; return $archive }
        StopServices = { foreach ($name in @('BookmarksSync','BookmarksSyncUpdater')) { $s=Get-Service $name -ErrorAction SilentlyContinue; if ($s -and $s.Status -ne 'Stopped') { Stop-Service $name -Force; (Get-Service $name).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(60)) } } }
        InstallAssets = { param($release,$rclone,$p) Copy-Item (Join-Path $release 'bookmarksync-windows-amd64.exe') (Join-Path $p.install 'bookmarksync.exe') -Force; Copy-Item (Join-Path $release 'bookmarksync-updater-windows-amd64.exe') (Join-Path $p.install 'bookmarksync-updater.exe') -Force; Copy-Item (Join-Path $release 'geoipupdate-windows-amd64.exe') (Join-Path $p.install 'geoipupdate.exe') -Force; Copy-Item (Join-Path $release 'GEOIPUPDATE-LICENSES.txt') (Join-Path $p.install 'GEOIPUPDATE-LICENSES.txt') -Force; Copy-Item (Join-Path $release 'SHA256SUMS') (Join-Path $p.install 'SHA256SUMS') -Force; Copy-Item (Join-Path $rclone 'rclone.exe') (Join-Path $p.install 'rclone.exe') -Force; $license=Join-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) 'third_party\rclone-COPYING'; if (Test-Path $license) { Copy-Item $license (Join-Path $p.install 'rclone-COPYING') -Force } }
        StartServices = { foreach ($name in @('BookmarksSyncUpdater','BookmarksSync')) { $s=Get-Service $name -ErrorAction SilentlyContinue; if ($s -and $s.Status -ne 'Running') { Start-Service $name } } }
        WaitReady = { param($url) $until=(Get-Date).AddSeconds(45); do { try { $r=Invoke-WebRequest -UseBasicParsing -Uri $url -TimeoutSec 2; if ($r.StatusCode -eq 200) { return } } catch {}; Start-Sleep -Milliseconds 500 } while ((Get-Date) -lt $until); throw 'BookmarksSync did not become ready.' }
        RestoreSnapshot = { param($archive,$p) if ((Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant() -cne (Get-Content -Raw ($archive+'.sha256'))) { throw 'Rollback snapshot checksum is invalid.' }; $stage=Join-Path $p.staging ('restore-' + [guid]::NewGuid().ToString('N')); New-Item -ItemType Directory -Force $stage | Out-Null; Expand-Archive -LiteralPath $archive -DestinationPath $stage -Force; $manifest=Get-Content -Raw (Join-Path $stage 'manifest.json') | ConvertFrom-Json; foreach ($entry in $manifest.files) { if ($entry.path -match '(^/|(^|/)\.\.(\/|$)|\\)' -or $entry.path -eq 'manifest.json') { throw 'Rollback snapshot manifest is invalid.' }; $file=Join-Path $stage ($entry.path.Replace('/', '\')); if (-not (Test-Path -LiteralPath $file -PathType Leaf) -or (Get-FileHash $file -Algorithm SHA256).Hash.ToLowerInvariant() -cne $entry.sha256) { throw 'Rollback snapshot file verification failed.' } }; if ($manifest.tag -notmatch '^build-[0-9a-f]{40}$') { throw 'Rollback snapshot release tag is invalid.' }; Remove-Item -LiteralPath $p.data -Recurse -Force; Move-Item (Join-Path $stage 'data') $p.data; Copy-Item (Join-Path $stage 'bookmarksync.env') $p.env -Force; if (Test-Path (Join-Path $stage 'rclone')) { Remove-Item -LiteralPath $p.rclone -Recurse -Force -ErrorAction SilentlyContinue; Move-Item (Join-Path $stage 'rclone') $p.rclone }; Remove-Item -LiteralPath $p.install -Recurse -Force; Move-Item (Join-Path $stage 'install') $p.install; Set-BookmarksSyncPathAcls $p; Set-InstallAcl $p.install; Remove-Item $stage -Recurse -Force }
        ConfigureServices = { param($install,$data,$envFile,$repair) Install-Services $install $data $envFile $repair }
        PruneSnapshots = { param($p) $snapshots=@(Get-ChildItem -LiteralPath $p.snapshots -Filter 'snapshot-*.zip' -File | Sort-Object LastWriteTimeUtc -Descending); foreach ($old in $snapshots | Select-Object -Skip 3) { Remove-Item -LiteralPath $old.FullName -Force; Remove-Item -LiteralPath ($old.FullName+'.sha256') -Force -ErrorAction SilentlyContinue } }
        CreateAdmin = { param($p,$user,$password) if ($user -notmatch '^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$' -or $password.Length -lt 12) { throw 'Choose a valid administrator name and a password of at least 12 characters.' }; $exe=Join-Path $p.install 'bookmarksync.exe'; $psi=[Diagnostics.ProcessStartInfo]::new($exe); $psi.Arguments='user create --username ' + $user + ' --password-stdin --admin --data-dir "' + $p.data + '"'; $psi.UseShellExecute=$false; $psi.RedirectStandardInput=$true; $psi.RedirectStandardOutput=$true; $psi.RedirectStandardError=$true; $proc=[Diagnostics.Process]::Start($psi); $proc.StandardInput.WriteLine($password); $proc.StandardInput.Close(); $out=$proc.StandardOutput.ReadToEnd(); $err=$proc.StandardError.ReadToEnd(); $proc.WaitForExit(); if ($proc.ExitCode -ne 0) { throw 'Initial administrator could not be created. Review local setup and retry.' } }
        CleanupStage = { param($p) if ($p -and (Test-Path $p)) { Remove-Item -LiteralPath $p -Recurse -Force } }
    }
}

Export-ModuleMember -Function Assert-ApprovedTag,Get-RequiredAssets,Get-VerifiedRelease,Get-AppPathAccessPolicies,Invoke-BookmarksSyncInstall,New-DefaultOperations

