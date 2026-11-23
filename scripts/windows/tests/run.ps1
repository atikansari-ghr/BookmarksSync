$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $PSScriptRoot '..\BookmarksSync.Install.psm1') -Force
$script:Passed = 0
$script:Failed = 0

function Assert([bool]$Condition,[string]$Message) { if (-not $Condition) { throw $Message } }
function TestCase([string]$Name,[scriptblock]$Body) {
    try { & $Body; $script:Passed++; Write-Host "PASS $Name" -ForegroundColor Green }
    catch { $script:Failed++; Write-Host "FAIL $Name`: $($_.Exception.Message)" -ForegroundColor Red }
}
function New-FakeOperations([string]$Root,[hashtable]$State) {
    return @{
        FindGitHubCli = { 'fake-gh.exe' }
        ValidateGitHubCli = { param($gh) }
        SelectLatestRelease = { param($gh) $State.Events.Add('select-latest'); return 'build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' }
        ValidateServices = { param($i,$r) if ($State.Unmanaged) { throw 'unmanaged service' }; $State.Events.Add('preflight') }
        RollbackFresh = { param($p) $State.Events.Add('rollback-fresh') }
        VerifyRelease = { param($tag,$stage,$gh) $State.Events.Add('verify'); if ($State.VerifyFails) { throw 'verification failed' }; New-Item -ItemType Directory -Force $stage | Out-Null; return $stage }
        PrepareRclone = { param($dest) $State.Events.Add('rclone'); New-Item -ItemType Directory -Force $dest | Out-Null; Set-Content (Join-Path $dest 'rclone.exe') 'fixture' }
        ProtectPaths = { param($paths) $State.Events.Add('acl') }
        ProtectInstall = { param($path) $State.Events.Add('install-acl') }
        CreateSnapshot = { param($paths,$tag) $State.Events.Add('snapshot'); if ($State.SnapshotFails) { throw 'disk full' }; return 'isolated-snapshot.zip' }
        StopServices = { $State.Events.Add('stop') }
        InstallAssets = { param($stage,$rclone,$paths) $State.Events.Add('install-assets'); New-Item -ItemType Directory -Force $paths.install | Out-Null; Set-Content (Join-Path $paths.install 'bookmarksync.exe') 'new-server'; Set-Content (Join-Path $paths.install 'bookmarksync-updater.exe') 'new-updater' }
        ConfigureServices = { param($i,$d,$e,$r) $State.Events.Add('services') }
        PruneSnapshots = { param($paths) $State.Events.Add('prune') }
        StartServices = { $State.Events.Add('start') }
        WaitReady = { param($url) $State.Events.Add('ready'); if ($State.ReadyFails) { throw 'not ready' } }
        RestoreSnapshot = { param($archive,$paths) $State.Events.Add('restore'); Set-Content (Join-Path $paths.install 'bookmarksync.exe') 'old-server' }
        CreateAdmin = { param($paths,$user,$password) $State.AdminUser=$user; $State.PasswordSeen=$password }
        CleanupStage = { param($path) if ($path -and (Test-Path $path)) { Remove-Item -LiteralPath $path -Recurse -Force } }
    }
}
function global:Read-Host { param([string]$Prompt,[switch]$AsSecureString) if ($AsSecureString) { return ConvertTo-SecureString 'TestOnlyPassword123' -AsPlainText -Force }; return 'admin' }

$tag='build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
TestCase 'invalid tag rejected before any operation' {
    $state=@{Events=[Collections.Generic.List[string]]::new()}; $ops=New-FakeOperations $TestDrive $state
    try { Invoke-BookmarksSyncInstall -Action upgrade -Tag latest -Root (Join-Path $TestDrive 'root') -InstallDir (Join-Path $TestDrive 'install') -Operations $ops; throw 'invalid tag accepted' } catch { Assert ($state.Events.Count -eq 0) 'operations ran before invalid tag rejection' }
}
TestCase 'release verification failure leaves host paths untouched' {
    $root=Join-Path ([IO.Path]::GetTempPath()) ('bms-test-'+[guid]::NewGuid().ToString('N')); $install=$root+'-install'; $state=@{Events=[Collections.Generic.List[string]]::new();VerifyFails=$true}; $ops=New-FakeOperations $root $state
    try { Invoke-BookmarksSyncInstall -Action install -Root $root -InstallDir $install -Operations $ops; throw 'verification failure ignored' } catch { Assert (-not (Test-Path $root)) 'root mutated before release verification' }
}
TestCase 'unmanaged service fails before download and mutation' {
    $root=Join-Path ([IO.Path]::GetTempPath()) ('bms-test-'+[guid]::NewGuid().ToString('N')); $install=$root+'-install'; $state=@{Events=[Collections.Generic.List[string]]::new();Unmanaged=$true}; $ops=New-FakeOperations $root $state
    try { Invoke-BookmarksSyncInstall -Action install -Root $root -InstallDir $install -Operations $ops; throw 'unmanaged service accepted' } catch { Assert (-not ($state.Events -contains 'verify')) 'release fetched before service preflight'; Assert (-not (Test-Path $root)) 'root mutated before service preflight' }
}
TestCase 'upgrade snapshots before stopping and restores after readiness failure' {
    $root=Join-Path ([IO.Path]::GetTempPath()) ('bms-test-'+[guid]::NewGuid().ToString('N')); $install=$root+'-install'; New-Item -ItemType Directory -Force $root,$install | Out-Null; Set-Content (Join-Path $install 'bookmarksync.exe') 'old-server'; Set-Content (Join-Path $root 'bookmarksync.env') 'private config'; New-Item -ItemType Directory -Force (Join-Path $root 'data') | Out-Null; Set-Content (Join-Path $root 'data\bookmarksync.db') 'old data'
    $state=@{Events=[Collections.Generic.List[string]]::new();ReadyFails=$true}; $ops=New-FakeOperations $root $state
    try { Invoke-BookmarksSyncInstall -Action upgrade -Tag $tag -Root $root -InstallDir $install -Operations $ops; throw 'readiness failure ignored' } catch { Assert ($state.Events.IndexOf('stop') -lt $state.Events.IndexOf('snapshot')) 'snapshot created before service stopped'; Assert ($state.Events -contains 'restore') 'snapshot was not restored'; Assert ((Get-Content -Raw (Join-Path $root 'bookmarksync.env')) -eq "private config`r`n") 'configuration changed on rollback'; Assert ((Get-Content -Raw (Join-Path $root 'data\bookmarksync.db')) -eq "old data`r`n") 'data changed on rollback'; Assert ((Get-Content -Raw (Join-Path $install 'bookmarksync.exe')) -eq "old-server`r`n") 'server binary was not restored' }
    Remove-Item $root,$install -Recurse -Force
}
TestCase 'fresh install protects configuration and passes first-admin password through the operation boundary' {
    $root=Join-Path ([IO.Path]::GetTempPath()) ('bms-test-'+[guid]::NewGuid().ToString('N')); $install=$root+'-install'; $state=@{Events=[Collections.Generic.List[string]]::new()}; $ops=New-FakeOperations $root $state
    Invoke-BookmarksSyncInstall -Action install -PublicUrl 'https://bookmarks.example.com' -ManagementCidrs '192.168.1.0/24' -Root $root -InstallDir $install -Operations $ops
    $envText=Get-Content -Raw (Join-Path $root 'bookmarksync.env')
    Assert ($envText.Contains('BOOKMARKSYNC_MANAGEMENT_CIDRS=127.0.0.1/32,::1/128,192.168.1.0/24')) 'loopback or requested management CIDRs absent'
    Assert ($envText.Contains('BOOKMARKSYNC_RCLONE_CONFIG=')) 'rclone config path missing'
    Assert ($state.Events.IndexOf('verify') -lt $state.Events.IndexOf('acl') -and $state.Events.IndexOf('rclone') -lt $state.Events.IndexOf('acl')) 'host mutation occurred before release and rclone verification'
    Assert ($state.AdminUser -eq 'admin' -and $state.PasswordSeen -eq 'TestOnlyPassword123') 'initial admin input was not delivered'
    Remove-Item $root -Recurse -Force
}
TestCase 'management CIDR and public URL are rejected before external operations' {
    $root=Join-Path ([IO.Path]::GetTempPath()) ('bms-test-'+[guid]::NewGuid().ToString('N')); $state=@{Events=[Collections.Generic.List[string]]::new()}; $ops=New-FakeOperations $root $state
    try { Invoke-BookmarksSyncInstall -Action install -ManagementCidrs '0.0.0.0/99' -Root $root -InstallDir ($root+'-install') -Operations $ops; throw 'invalid CIDR accepted' } catch { Assert ($state.Events.Count -eq 0) 'external operations ran before CIDR validation' }
    try { Invoke-BookmarksSyncInstall -Action install -PublicUrl 'http://example.com' -Root $root -InstallDir ($root+'-install') -Operations $ops; throw 'insecure public URL accepted' } catch { Assert ($state.Events.Count -eq 0) 'external operations ran before origin validation' }
}
TestCase 'release verifier rejects changed checksum and failed attestation' {
    $base=Join-Path ([IO.Path]::GetTempPath()) ('bms-verify-'+[guid]::NewGuid().ToString('N')); $fixture=Join-Path $base 'fixture'; $stage=Join-Path $base 'stage'; New-Item -ItemType Directory -Force $fixture | Out-Null
    $commit=$tag.Substring(6); $bodies=@{ 'bookmarksync-windows-amd64.exe'='server fixture'; 'bookmarksync-updater-windows-amd64.exe'='updater fixture'; 'bookmarksync-linux-amd64'='linux fixture'; 'BUILD_INFO.txt'="commit=$commit`nbuild_date=2026-11-23T00:00:00Z`nversion=0.0.0-$($commit.Substring(0,12))`n" }
    $sums=@(); foreach($name in $bodies.Keys) { Set-Content -NoNewline -Encoding ascii (Join-Path $fixture $name) $bodies[$name]; $hash=(Get-FileHash (Join-Path $fixture $name) -Algorithm SHA256).Hash.ToLowerInvariant(); $sums += "$hash  $name" }; Set-Content -Encoding ascii (Join-Path $fixture 'SHA256SUMS') $sums
    $fake=Join-Path $base 'gh.ps1'; @'
param([string]$Command,[string]$Subcommand,[Parameter(ValueFromRemainingArguments=$true)][string[]]$Rest)
if ($Command -eq 'release' -and $Subcommand -eq 'view') {
  $assets=@('bookmarksync-windows-amd64.exe','bookmarksync-updater-windows-amd64.exe','bookmarksync-linux-amd64','BUILD_INFO.txt','SHA256SUMS') | ForEach-Object { [ordered]@{name=$_;size=10} }
  [ordered]@{tagName=$env:FAKE_RELEASE_TAG;isDraft=$false;isPrerelease=$true;assets=$assets} | ConvertTo-Json -Compress -Depth 4; $global:LASTEXITCODE=0; return
}
if ($Command -eq 'release' -and $Subcommand -eq 'download') {
  $dest=$Rest[$Rest.IndexOf('--dir')+1]; $asset=$Rest[$Rest.IndexOf('--pattern')+1]; Copy-Item (Join-Path $env:FAKE_RELEASE_FIXTURE $asset) (Join-Path $dest $asset) -Force; $global:LASTEXITCODE=0; return
}
if ($Command -eq 'attestation' -and $env:FAKE_ATTEST_FAIL -eq '1') { $global:LASTEXITCODE=1; return }
$global:LASTEXITCODE=0; return
'@ | Set-Content -Encoding ascii $fake
    $env:FAKE_RELEASE_TAG=$tag; $env:FAKE_RELEASE_FIXTURE=$fixture; $env:FAKE_ATTEST_FAIL='0'
    $verified=Get-VerifiedRelease $tag $stage $fake
    Assert ((Test-Path (Join-Path $verified 'bookmarksync-updater-windows-amd64.exe'))) 'valid release was not staged'
    $env:FAKE_ATTEST_FAIL='1'; $badStage=Join-Path $base 'bad-stage'; try { Get-VerifiedRelease $tag $badStage $fake | Out-Null; throw 'failed attestation accepted' } catch { Assert ($_.Exception.Message -notlike 'failed attestation accepted') 'attestation failure was ignored' }
    Set-Content (Join-Path $fixture 'bookmarksync-windows-amd64.exe') 'tampered'; $env:FAKE_ATTEST_FAIL='0'; $tamperStage=Join-Path $base 'tamper-stage'; try { Get-VerifiedRelease $tag $tamperStage $fake | Out-Null; throw 'tampered release accepted' } catch { Assert ($_.Exception.Message -notlike 'tampered release accepted') 'checksum failure was ignored' }
    Remove-Item Env:FAKE_RELEASE_TAG,Env:FAKE_RELEASE_FIXTURE,Env:FAKE_ATTEST_FAIL; Remove-Item $base -Recurse -Force
}
TestCase 'snapshot archive includes data, protected settings, rclone config and binary hashes' {
    $root=Join-Path ([IO.Path]::GetTempPath()) ('bms-snapshot-'+[guid]::NewGuid().ToString('N')); $install=Join-Path $root 'install'; $data=Join-Path $root 'data'; $rclone=Join-Path $root 'rclone'; $snapshots=Join-Path $root 'snapshots'; $staging=Join-Path $root 'staging'
    New-Item -ItemType Directory -Force $install,$data,$rclone | Out-Null; Set-Content (Join-Path $install 'bookmarksync.exe') 'server'; Set-Content (Join-Path $install 'bookmarksync-updater.exe') 'updater'; Set-Content (Join-Path $data 'bookmarksync.db') 'database'; Set-Content (Join-Path $root 'bookmarksync.env') 'secret-setting'; Set-Content (Join-Path $rclone 'rclone.conf') 'provider-token'
    $paths=[ordered]@{root=$root;install=$install;data=$data;env=(Join-Path $root 'bookmarksync.env');rclone=$rclone;snapshots=$snapshots;staging=$staging}; $ops=New-DefaultOperations; $archive=& $ops.CreateSnapshot $paths $tag
    Assert (Test-Path ($archive+'.sha256')) 'archive checksum sidecar missing'
    $extract=Join-Path $root 'check'; Expand-Archive -LiteralPath $archive -DestinationPath $extract
    $manifest=Get-Content -Raw (Join-Path $extract 'manifest.json') | ConvertFrom-Json
    Assert ($manifest.tag -eq $tag) 'snapshot is not tied to the approved release'
    foreach($name in @('data/bookmarksync.db','bookmarksync.env','rclone/rclone.conf','install/bookmarksync.exe','install/bookmarksync-updater.exe')) { Assert (@($manifest.files | Where-Object { $_.path -eq $name }).Count -eq 1) "snapshot manifest missing $name" }
    $before=Get-Content -Raw (Join-Path $data 'bookmarksync.db'); Set-Content -LiteralPath $archive -Value 'tampered' -NoNewline -Encoding ascii; Assert ((Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant() -cne (Get-Content -Raw ($archive+'.sha256'))) 'test archive was not actually tampered'; try { & $ops.RestoreSnapshot $archive $paths; throw 'tampered snapshot accepted' } catch { Assert ($_.Exception.Message -notlike 'tampered snapshot accepted') 'tampered archive was accepted' }
    Assert ((Get-Content -Raw (Join-Path $data 'bookmarksync.db')) -eq $before) 'rejected snapshot changed data'
    Remove-Item $root -Recurse -Force
}
TestCase 'asset install includes the pinned rclone executable and its license' {
    $root=Join-Path ([IO.Path]::GetTempPath()) ('bms-assets-'+[guid]::NewGuid().ToString('N')); $stage=Join-Path $root 'release'; $rclone=Join-Path $root 'rclone'; $install=Join-Path $root 'install'; New-Item -ItemType Directory -Force $stage,$rclone,$install | Out-Null
    foreach($asset in @('bookmarksync-windows-amd64.exe','bookmarksync-updater-windows-amd64.exe')) { Set-Content (Join-Path $stage $asset) $asset }
    Set-Content (Join-Path $rclone 'rclone.exe') 'pinned-rclone-fixture'
    $paths=[ordered]@{install=$install}; $ops=New-DefaultOperations; & $ops.InstallAssets $stage $rclone $paths
    Assert (Test-Path (Join-Path $install 'bookmarksync.exe')) 'server binary not installed'
    Assert (Test-Path (Join-Path $install 'bookmarksync-updater.exe')) 'updater binary not installed'
    Assert ((Get-Content -Raw (Join-Path $install 'rclone.exe')) -eq "pinned-rclone-fixture`r`n") 'rclone binary not installed'
    Assert (Test-Path (Join-Path $install 'rclone-COPYING')) 'rclone license not installed'
    Remove-Item $root -Recurse -Force
}
TestCase 'junction install root is rejected before any external operation' {
    $base=Join-Path ([IO.Path]::GetTempPath()) ('bms-junction-'+[guid]::NewGuid().ToString('N')); $target=Join-Path $base 'target'; $link=Join-Path $base 'link'; New-Item -ItemType Directory -Force $target | Out-Null
    & cmd.exe /c "mklink /J `"$link`" `"$target`"" | Out-Null
    if ($LASTEXITCODE -ne 0) { Remove-Item $base -Recurse -Force; throw 'junction creation unavailable on this runner' }
    $state=@{Events=[Collections.Generic.List[string]]::new()}; $ops=New-FakeOperations $base $state
    try { Invoke-BookmarksSyncInstall -Action install -Root $link -InstallDir ($base+'-install') -Operations $ops; throw 'junction accepted' } catch { Assert ($_.Exception.Message -notlike 'junction accepted') 'junction path was accepted'; Assert ($state.Events.Count -eq 0) 'external operation ran through junction' }
    Remove-Item $link -Force; Remove-Item $base -Recurse -Force
}
TestCase 'repair reruns managed service configuration' {
    $root=Join-Path ([IO.Path]::GetTempPath()) ('bms-test-'+[guid]::NewGuid().ToString('N')); $install=$root+'-install'; New-Item -ItemType Directory -Force $root,$install | Out-Null; Set-Content (Join-Path $install 'bookmarksync.exe') 'old-server'; Set-Content (Join-Path $root 'bookmarksync.env') 'private config'
    $state=@{Events=[Collections.Generic.List[string]]::new()}; $ops=New-FakeOperations $root $state
    Invoke-BookmarksSyncInstall -Action repair -Tag $tag -Root $root -InstallDir $install -Operations $ops
    Invoke-BookmarksSyncInstall -Action repair -Tag $tag -Root $root -InstallDir $install -Operations $ops
    Assert (@($state.Events | Where-Object { $_ -eq 'services' }).Count -eq 2) 'repeat repair did not reconfigure services'; Remove-Item $root,$install -Recurse -Force
}
TestCase 'snapshot failure restarts existing services before returning' {
    $root=Join-Path ([IO.Path]::GetTempPath()) ('bms-test-'+[guid]::NewGuid().ToString('N')); $install=$root+'-install'; New-Item -ItemType Directory -Force $root,$install | Out-Null; Set-Content (Join-Path $install 'bookmarksync.exe') 'old-server'; Set-Content (Join-Path $root 'bookmarksync.env') 'private config'; $state=@{Events=[Collections.Generic.List[string]]::new();SnapshotFails=$true}; $ops=New-FakeOperations $root $state
    try { Invoke-BookmarksSyncInstall -Action upgrade -Tag $tag -Root $root -InstallDir $install -Operations $ops; throw 'snapshot failure ignored' } catch { Assert ($state.Events -contains 'start') 'services were not restarted after snapshot failure'; Assert (-not ($state.Events -contains 'install-assets')) 'files changed without a verified snapshot' }
    Remove-Item $root,$install -Recurse -Force
}
TestCase 'failed fresh service startup invokes exact-path cleanup' {
    $root=Join-Path ([IO.Path]::GetTempPath()) ('bms-test-'+[guid]::NewGuid().ToString('N')); $install=$root+'-install'; $state=@{Events=[Collections.Generic.List[string]]::new();ReadyFails=$true}; $ops=New-FakeOperations $root $state
    try { Invoke-BookmarksSyncInstall -Action install -Root $root -InstallDir $install -Operations $ops; throw 'startup failure ignored' } catch { Assert ($state.Events -contains 'rollback-fresh') 'fresh install cleanup did not run' }
    Remove-Item $root -Recurse -Force -ErrorAction SilentlyContinue
}
if ($script:Failed -gt 0) { throw "$($script:Failed) Windows installer test(s) failed; $($script:Passed) passed." }
Write-Host "Windows installer tests: $($script:Passed) passed." -ForegroundColor Green

