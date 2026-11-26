$ErrorActionPreference = "Stop"

$Repo = "akhanatoon/quench"
$InstallDir = if ($env:QUENCH_INSTALL_DIR) { $env:QUENCH_INSTALL_DIR } else { Join-Path $HOME ".local\bin" }
$Arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }

$Release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
$Tag = $Release.tag_name
$Ver = $Tag.TrimStart("v")
$Url = "https://github.com/$Repo/releases/download/$Tag/quench_${Ver}_windows_${Arch}.zip"

$Tmp = Join-Path $env:TEMP ("quench-install-" + [guid]::NewGuid().ToString("N").Substring(0,6))
New-Item -ItemType Directory -Path $Tmp -Force | Out-Null
try {
    Write-Host "Downloading quench $Tag (windows/$Arch)..."
    $Zip = Join-Path $Tmp "quench.zip"
    Invoke-WebRequest -Uri $Url -OutFile $Zip
    Expand-Archive -Path $Zip -DestinationPath $Tmp -Force

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    Copy-Item -Path (Join-Path $Tmp "quench.exe") -Destination (Join-Path $InstallDir "quench.exe") -Force
    Write-Host "Installed quench.exe to $InstallDir\quench.exe"

    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($UserPath -notlike "*$InstallDir*") {
        [Environment]::SetEnvironmentVariable("Path", "$UserPath;$InstallDir", "User")
        Write-Host "Added $InstallDir to your User PATH (restart your terminal to use 'quench')."
    }
} finally {
    Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue
}
