#Requires -Modules ActiveDirectory
<#
.SYNOPSIS
    Creates a dedicated AD service account for Camera Dashboard.

.DESCRIPTION
    Creates a service account with read-only access to AD user objects.
    The account is used for:
      - LDAP bind (service account authentication)
      - User authentication (bind-as-user to verify credentials)
      - Reading user attributes (displayName, department, memberOf, etc.)

    No special AD delegation is needed — standard domain user read
    permissions are sufficient for all Camera Dashboard operations.

.NOTES
    Run this on a domain controller or a machine with RSAT-AD-PowerShell.
    Must be run as a Domain Admin or account with rights to create users.
#>

param(
    [string]$AccountName      = "CameraDashboardSvc",
    [string]$DisplayName      = "Camera Dashboard Service Account",
    [string]$Description      = "Service account for Camera Dashboard LDAP authentication",
    [string]$OUPath           = "",   # Auto-detects Service Accounts OU if blank
    [string]$Password         = ""    # Auto-generates a 32-char password if blank
)

$ErrorActionPreference = "Stop"

# --- Auto-detect Service Accounts OU if not specified ---
if (-not $OUPath) {
    $searchOUs = @("Service Accounts", "ServiceAccounts", "Svc Accounts")
    $domain = Get-ADDomain
    foreach ($name in $searchOUs) {
        $ou = Get-ADOrganizationalUnit -Filter "Name -eq '$name'" -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($ou) {
            $OUPath = $ou.DistinguishedName
            Write-Host "Found OU: $OUPath" -ForegroundColor Green
            break
        }
    }
    if (-not $OUPath) {
        # Fall back to default Users container
        $OUPath = $domain.UsersContainer
        Write-Host "No Service Accounts OU found, using: $OUPath" -ForegroundColor Yellow
    }
}

# --- Generate password if not specified ---
if (-not $Password) {
    $chars = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!@#$%&*"
    $Password = -join (1..32 | ForEach-Object { $chars[(Get-Random -Maximum $chars.Length)] })
}

# --- Check if account already exists ---
$existing = Get-ADUser -Filter "SamAccountName -eq '$AccountName'" -ErrorAction SilentlyContinue
if ($existing) {
    Write-Host "Account '$AccountName' already exists:" -ForegroundColor Red
    Write-Host "  DN: $($existing.DistinguishedName)"
    Write-Host "  Enabled: $($existing.Enabled)"
    Write-Host ""
    Write-Host "To reset the password, run:" -ForegroundColor Yellow
    Write-Host "  Set-ADAccountPassword -Identity '$AccountName' -Reset -NewPassword (ConvertTo-SecureString '<new-password>' -AsPlainText -Force)"
    exit 1
}

# --- Create the service account ---
$securePassword = ConvertTo-SecureString $Password -AsPlainText -Force
$domain = Get-ADDomain
$upn = "$AccountName@$($domain.DNSRoot)"

New-ADUser `
    -Name $DisplayName `
    -SamAccountName $AccountName `
    -UserPrincipalName $upn `
    -DisplayName $DisplayName `
    -Description $Description `
    -Path $OUPath `
    -AccountPassword $securePassword `
    -Enabled $true `
    -PasswordNeverExpires $true `
    -CannotChangePassword $true `
    -ChangePasswordAtLogon $false

Write-Host ""
Write-Host "=== Service Account Created ===" -ForegroundColor Green
Write-Host "  Account:  $AccountName"
Write-Host "  UPN:      $upn"
Write-Host "  OU:       $OUPath"
Write-Host "  Password: $Password"
Write-Host ""
Write-Host "=== Update camera_adlogin.json ===" -ForegroundColor Cyan
Write-Host @"
{
  "server": "$($domain.PDCEmulator.Split('.')[0]).$($domain.DNSRoot)",
  "port": 389,
  "use_ssl": false,
  "skip_tls_verify": false,
  "bind_dn": "$upn",
  "bind_password": "$Password",
  "base_dn": "$($domain.DistinguishedName)",
  "user_search_base": "$($domain.DistinguishedName)",
  "user_filter": "(&(objectClass=user)(objectCategory=person))",
  "attributes": [
    "sAMAccountName", "displayName", "mail", "memberOf",
    "userAccountControl", "whenCreated", "lastLogon",
    "distinguishedName", "description", "department",
    "title", "manager", "telephoneNumber", "mobile",
    "ipPhone", "physicalDeliveryOfficeName", "company",
    "givenName", "sn", "objectGUID", "userPrincipalName",
    "accountExpires", "homePhone", "pager",
    "facsimileTelephoneNumber", "cn"
  ]
}
"@
Write-Host ""
Write-Host "Then deploy with: make prod-deploy-config" -ForegroundColor Yellow
