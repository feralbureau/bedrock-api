param(
    [switch]$clean
)

$badPatterns = @(
    "// ===",        # decors
    "/\* ---",       # blocks
    "// [A-Z]",      # uppercase neuroslop
    "Ђ"          # idk how it appeared in codebase  
)

# исключаем сгенерированное говно
$files = Get-ChildItem -Recurse -Filter "*.go" -Exclude "*_grpc.pb.go","*.pb.go"
$errorCount = 0

foreach ($file in $files) {
    $lines = Get-Content $file.FullName
    $newContent = @()
    $isDirty = $false

    for ($i = 0; $i -lt $lines.Count; $i++) {
        $line = $lines[$i]
        $isBadLine = $false

        foreach ($pattern in $badPatterns) {
            # skip TODO (святое)
            if ($line -match "// TODO") { continue }

            # skip go:generate directives
            if ($line -match "//go:") { continue }

            # check bad pattern
            if ($line -cmatch $pattern) {
                $isBadLine = $true
                break
            }
        }

        if ($isBadLine) {
            if ($clean) {
                # Режим чистки: просто не добавляем строку в новый контент
                # Write-Host "[X] Deleting from $($file.Name): $($line.Trim())" -ForegroundColor DarkGray
                $isDirty = $true
            } else {
                # Режим проверки: орем
                Write-Host "[-] fuckup found in $($file.Name):$($i+1)" -ForegroundColor Red
                Write-Host "    line: $($line.Trim())" -ForegroundColor DarkRed
                $errorCount++
                $newContent += $line # в режиме проверки оставляем как есть
            }
        } else {
            # Хорошая строка, оставляем
            $newContent += $line
        }
    }

    # Если были изменения и включен режим чистки - перезаписываем файл
    if ($clean -and $isDirty) {
        $newContent | Set-Content -Path $file.FullName -Encoding UTF8
        Write-Host "[+] Cleaned $($file.Name)" -ForegroundColor Cyan
    }
}

if ($clean) {
    Write-Host "`n[+] Cleanup complete. Run without -clean to verify." -ForegroundColor Green
    exit 0
}

if ($errorCount -gt 0) {
    Write-Host "`n[!] lint failed with $errorCount errors" -ForegroundColor Red
    exit 1
} else {
    Write-Host "[+] all checks passed!" -ForegroundColor Green
}
