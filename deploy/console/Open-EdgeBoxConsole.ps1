param([ValidateRange(1024,65535)][int]$Port = 18080,
      [string]$Config = (Join-Path $env:USERPROFILE '.ssh/edgebox_config'))
$ErrorActionPreference = 'Stop'
Write-Host "After SSH connects, open http://127.0.0.1:$Port . Stop with Ctrl+C."
& ssh -F $Config -o ExitOnForwardFailure=yes -N -L "127.0.0.1:${Port}:127.0.0.1:8080" n1-remote
exit $LASTEXITCODE
