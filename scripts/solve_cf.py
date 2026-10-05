#!/usr/bin/env python3
"""Solver challenge Cloudflare on-demand utk jalur hybrid scraper_v2 (doc 35).

Dipanggil Go (internal/garuda/hybrid.go runSolver):
    python scripts/solve_cf.py <url>

Strategi (approve user 4 Okt 2026 — teruji di eksperimen doc 34 §6c):
nodriver HEADED (headless gagal — UA HeadlessChrome bocor) → Chrome asli
menyelesaikan managed challenge ±7,5 dtk → ambil cf_clearance + UA asli.

Kontrak stdout: SATU baris JSON
    {"ok":true, "cookie":"cf_clearance=...", "ua":"Chrome/...", "status":200, "ms":7500, "err":""}
    {"ok":false, "cookie":"", "ua":"", "status":0, "ms":0, "err":"..."}
Semua noise/diagnostic ke STDERR (Go hanya membaca stdout).
Exit code: 0 = ok, 1 = gagal (JSON tetap dicetak dulu).
"""
import asyncio
import json
import sys
import time

CHALLENGE_MARK = "just a moment"
WAIT_S = 45.0  # batas tunggu solve (fakta: lolos ±7,5 s)
SETTLE_S = 1.5  # beri waktu cookie final tersimpan


def emit(obj) -> None:
    sys.stdout.write(json.dumps(obj, ensure_ascii=True) + "\n")
    sys.stdout.flush()


async def main(url: str) -> int:
    t0 = time.time()
    try:
        import nodriver as nd
    except ImportError as e:
        emit({"ok": False, "cookie": "", "ua": "", "status": 0,
              "ms": 0, "err": f"nodriver tak terpasang: {e}"})
        return 1

    browser = None
    try:
        browser = await nd.start(headless=False)  # headed = wajib (fakta §6c)
        page = await browser.get(url)

        title = ""
        while time.time() - t0 < WAIT_S:
            try:
                title = str(await page.evaluate("document.title"))
            except Exception:
                title = ""
            if title and CHALLENGE_MARK not in title.lower():
                break
            await asyncio.sleep(0.4)

        await asyncio.sleep(SETTLE_S)
        title = str(await page.evaluate("document.title"))
        ua = str(await page.evaluate("navigator.userAgent"))

        cookie = ""
        try:
            for c in await page.send(nd.cdp.network.get_all_cookies()):
                if c.name == "cf_clearance":
                    cookie = f"cf_clearance={c.value}"
        except Exception as e:
            print(f"get_cookies: {e}", file=sys.stderr)

        ms = int((time.time() - t0) * 1000)
        challenged = CHALLENGE_MARK in title.lower()
        if challenged:
            emit({"ok": False, "cookie": "", "ua": ua, "status": 0, "ms": ms,
                  "err": f"challenge tak lolos dalam {WAIT_S:.0f} dtk (title={title!r})"})
            return 1
        emit({"ok": True, "cookie": cookie, "ua": ua, "status": 200,
              "ms": ms, "err": ""})
        return 0
    except Exception as e:
        ms = int((time.time() - t0) * 1000)
        emit({"ok": False, "cookie": "", "ua": "", "status": 0, "ms": ms,
              "err": f"{type(e).__name__}: {e}"})
        return 1
    finally:
        if browser is not None:
            try:
                browser.stop()
            except Exception:
                pass


if __name__ == "__main__":
    if len(sys.argv) != 2:
        emit({"ok": False, "cookie": "", "ua": "", "status": 0, "ms": 0,
              "err": "pakai: solve_cf.py <url>"})
        sys.exit(1)
    sys.exit(asyncio.run(main(sys.argv[1])))
