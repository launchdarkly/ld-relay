param(
    [string]$step,
    [string]$script
)

# Performs a delegated release step in a CircleCI Windows container using PowerShell. This
# mechanism is described in scripts/circleci/README.md. All of the necessary environment
# variables should already be in the generated CircleCI configuration.

$ErrorActionPreference = "Stop"

New-Item -Path "./artifacts" -ItemType "directory" -Force | Out-Null

$env:LD_RELEASE_TEMP_DIR = "$env:TEMP\project-releaser-temp"
New-Item -Path $env:LD_RELEASE_TEMP_DIR -ItemType "directory" -Force | Out-Null

Write-Host
Write-Host "[$step] executing $script"
& "./$script"
