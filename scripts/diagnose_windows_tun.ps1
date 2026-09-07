$ErrorActionPreference = 'Continue'
Write-Host '[SocksRevivePC] Windows TUN diagnostics'
Write-Host ''
Write-Host '== Wintun / tunnel adapters =='
Get-NetAdapter | Where-Object { $_.Name -like '*wintun*' -or $_.InterfaceDescription -like '*Wintun*' -or $_.InterfaceDescription -like '*WireGuard*' } | Format-List Name,InterfaceDescription,InterfaceIndex,Status,MacAddress,LinkSpeed
Write-Host ''
Write-Host '== Adapter IP addresses =='
Get-NetIPAddress | Where-Object { $_.InterfaceAlias -like '*wintun*' -or $_.InterfaceAlias -like '*WireGuard*' } | Format-Table InterfaceAlias,InterfaceIndex,AddressFamily,IPAddress,PrefixLength -AutoSize
Write-Host ''
Write-Host '== Split default routes =='
Get-NetRoute -DestinationPrefix '0.0.0.0/1','128.0.0.0/1','::/1','8000::/1' -ErrorAction SilentlyContinue | Sort-Object AddressFamily,DestinationPrefix,RouteMetric | Format-Table DestinationPrefix,InterfaceAlias,InterfaceIndex,NextHop,RouteMetric,AddressFamily -AutoSize
Write-Host ''
Write-Host '== Normal default routes =='
Get-NetRoute -DestinationPrefix '0.0.0.0/0','::/0' -ErrorAction SilentlyContinue | Sort-Object AddressFamily,RouteMetric,InterfaceMetric | Format-Table DestinationPrefix,InterfaceAlias,InterfaceIndex,NextHop,RouteMetric,InterfaceMetric,AddressFamily -AutoSize
Write-Host ''
Write-Host '== DNS servers =='
Get-DnsClientServerAddress | Where-Object { $_.InterfaceAlias -like '*wintun*' -or $_.InterfaceAlias -like '*WireGuard*' -or $_.AddressFamily -eq 2 } | Format-Table InterfaceAlias,AddressFamily,ServerAddresses -AutoSize
Write-Host ''
Write-Host 'If 0.0.0.0/1 and 128.0.0.0/1 do not point to the Wintun adapter, the app is not running as Administrator or Windows rejected the route.'
Write-Host ''
Write-Host '== Quick IPv6 test =='
try { Test-NetConnection -ComputerName '2606:4700:4700::1111' -Port 443 -InformationLevel Detailed } catch { Write-Host $_ }
Write-Host ''
Write-Host 'IPv6 note: if IPv6 support is OFF but leak protection is ON, ::/1 and 8000::/1 should point to Wintun and public IPv6 tests should fail instead of showing your ISP IPv6.'
