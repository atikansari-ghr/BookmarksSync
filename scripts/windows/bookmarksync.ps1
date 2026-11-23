[CmdletBinding()]
param(
    [Parameter(Position=0)][ValidateSet('install','upgrade','repair','help')][string]$Command = 'help',
    [string]$Tag,
    [string]$PublicUrl,
    [string]$ManagementCidrs
)

$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $PSScriptRoot 'BookmarksSync.Install.psm1') -Force
if ($Command -eq 'help') {
    @'
BookmarksSync Windows service installer (run from an elevated PowerShell)

  .\bookmarksync.ps1 install [-PublicUrl https://bookmarks.example.com] [-ManagementCidrs 192.168.1.0/24]
  .\bookmarksync.ps1 upgrade -Tag build-<full-40-character-commit-sha>
  .\bookmarksync.ps1 repair -Tag build-<full-40-character-commit-sha>

Install creates BookmarksSync and BookmarksSyncUpdater services and stores data
under ProgramData. GitHub CLI must already be installed. Use HTTPS through a
reverse proxy for remote access; no inbound firewall rules are created.
'@ | Write-Output
    exit 0
}
try {
    if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Run this command from an elevated PowerShell.' }
    Invoke-BookmarksSyncInstall -Action $Command -Tag $Tag -PublicUrl $PublicUrl -ManagementCidrs $ManagementCidrs
    Write-Output 'BookmarksSync installation operation completed.'
} catch {
    Write-Error $_.Exception.Message
    exit 1
}
