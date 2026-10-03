# Scrape penuh indeks affiliations SINTA (id|nama) ke data/stage1/affindex.tsv.
# Alat bantu Eksperimen C (doc 22): peta nama->id untuk resolusi publisher
# tanpa link profil pada kartu. Polite: 1 request / ~1,2 dtk.
$out = 'data\stage1\affindex.tsv'
$done = @{}
if (Test-Path $out) { Get-Content $out | ForEach-Object { $f = $_ -split "`t"; if ($f.Count -ge 2) { $done[$f[0]] = $true } } }
"lanjut dari $($done.Count) baris existing"
for ($p = 1; $p -le 558; $p++) {
    $r = $null
    for ($try = 1; $try -le 3 -and -not $r; $try++) {
        try { $r = Invoke-WebRequest -Uri "https://sinta.kemdiktisaintek.go.id/affiliations?page=$p" -UseBasicParsing -TimeoutSec 30 }
        catch { Start-Sleep -Seconds 2 }
    }
    if (-not $r) { "PAGE $p GAGAL"; continue }
    $ms = [regex]::Matches($r.Content, '<div class="affil-name"><a href="[^"]*/affiliations/profile/(\d+)">\s*([^<]+)')
    foreach ($m in $ms) {
        $id = $m.Groups[1].Value
        if ($done[$id]) { continue }
        $name = ($m.Groups[2].Value -replace '\s+', ' ').Trim()
        $name = $name -replace "`t", ' '
        Add-Content -Encoding utf8 $out "$id`t$name"
        $done[$id] = $true
    }
    if ($p % 50 -eq 0) { "p$p | unik=$($done.Count)" }
    Start-Sleep -Milliseconds 500
}
"SELESAI unik=$($done.Count)"
