# Pencocokan nama affiliation tanpa-link -> id profil (Eksperimen C, doc 22).
# Skor: 100=eknak norm, 80=profil mengandung nama kartu, 60+=token overlap >=0.6.
# Output: data/stage1/r6-match.tsv (review) + data/stage1/r6-candids.txt (id unik per baris).
$map = @{}
Get-Content data\stage1\affindex.tsv | ForEach-Object {
    $f = $_ -split "`t", 2
    if ($f.Count -eq 2) { $map[$f[0]] = $f[1] }
}
"map: $($map.Count)"

function Norm([string]$s) {
    if (-not $s) { return '' }
    $s = $s.ToLower() -replace '[^\p{L}\p{Nd}\s]', ' '
    $s = $s -replace '\s+', ' '
    return $s.Trim()
}
function Tokens([string]$s) {
    $stop = @('dan','the','of','for','and','pt','cv','yayasan','yay','press','publisher','international')
    return ((Norm $s) -split ' ') | Where-Object { $_.Length -gt 2 -and $_ -notin $stop }
}

$targets = @()
Get-Content data\stage1\r6-missing-detail.txt | ForEach-Object {
    if ($_ -match '^(\d+)\s*\|\s*(.+?)\s*\|') {
        $targets += [pscustomobject]@{ id = [int]$Matches[1]; name = $Matches[2] }
    }
}
$targets += [pscustomobject]@{ id = 18411; name = 'YAYASAN NUSANTARA CHILDREN OF THE CLOUDS' }
$targets += [pscustomobject]@{ id = 18544; name = 'YAYASAN YPMMA' }
"targets: $($targets.Count)"

$report = 'data\stage1\r6-match.tsv'
"target_id`tname`tncands`tcandidates" | Set-Content -Encoding utf8 $report
$candSet = @{}
foreach ($t in $targets) {
    $tn = Norm $t.name
    $tt = Tokens $t.name
    $scored = @()
    foreach ($k in $map.Keys) {
        $pn = Norm $map[$k]
        $score = 0
        if ($tn -and $pn -eq $tn) { $score = 100 }
        elseif ($tn -and $pn -like "*$tn*") { $score = 80 }
        elseif ($tt.Count -gt 0) {
            $pt = Tokens $map[$k]
            if ($pt.Count -gt 0) {
                $inter = @($tt | Where-Object { $pt -contains $_ }).Count
                $jac = $inter / [Math]::Max(1, [Math]::Min($tt.Count, $pt.Count))
                if ($jac -ge 0.6) { $score = [int]($jac * 60) }
            }
        }
        if ($score -gt 0) { $scored += [pscustomobject]@{ id = $k; score = $score; name = $map[$k] } }
    }
    $top = $scored | Sort-Object score -Descending | Select-Object -First 4
    foreach ($c in $top) { $candSet[[int]$c.id] = $true }
    $cstr = ($top | ForEach-Object { "$($_.id):$($_.score):$($_.name)" }) -join ' ;; '
    "$($t.id)`t$($t.name)`t$($top.Count)`t$cstr" | Add-Content -Encoding utf8 $report
}
($candSet.Keys | Sort-Object) -join "`n" | Set-Content -Encoding ascii data\stage1\r6-candids.txt
"kandidat unik: $($candSet.Count)"
"== tanpa kandidat =="
Get-Content $report | ForEach-Object { $f = $_ -split "`t"; if ($f.Count -ge 3 -and $f[2] -eq '0') { "$($f[0]) | $($f[1])" } }
"== dengan kandidat (preview 15) =="
Get-Content $report | Select-Object -Skip 1 -First 15