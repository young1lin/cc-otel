"""
Smoke test: load the dashboard, capture console errors and any failed network
requests, take a screenshot. Used by the ESM-refactor task list to verify each
checkpoint without a human eyeball.
"""
import sys
from pathlib import Path
from playwright.sync_api import sync_playwright


def main(url: str, screenshot_path: str) -> int:
    Path(screenshot_path).parent.mkdir(parents=True, exist_ok=True)
    errors: list[str] = []
    failed_requests: list[str] = []

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        ctx = browser.new_context(viewport={"width": 1440, "height": 900})
        page = ctx.new_page()

        page.on("console", lambda msg: errors.append(f"[{msg.type}] {msg.text}") if msg.type == "error" else None)
        page.on("pageerror", lambda exc: errors.append(f"[pageerror] {exc}"))
        page.on(
            "requestfailed",
            lambda req: failed_requests.append(f"{req.method} {req.url} :: {req.failure}"),
        )

        page.goto(url, wait_until="load", timeout=15000)
        # SSE keeps the connection open so 'networkidle' never triggers; wait fixed for JS to render.
        page.wait_for_timeout(2500)

        page.screenshot(path=screenshot_path, full_page=True)

        # Verify a few expected DOM nodes exist (proves the page actually rendered, not just loaded).
        checks = {
            "h-cost":     page.locator("#h-cost").count(),
            "h-input":    page.locator("#h-input").count(),
            "h-output":   page.locator("#h-output").count(),
            "main-chart": page.locator("#main-chart").count(),
            "daily-tbody":page.locator("#daily-tbody").count(),
        }

        browser.close()

    print(f"URL: {url}")
    print(f"Screenshot: {screenshot_path}")
    print(f"DOM checks: {checks}")
    print(f"Console errors: {len(errors)}")
    for e in errors:
        print(f"  - {e}")
    print(f"Failed requests: {len(failed_requests)}")
    for r in failed_requests:
        print(f"  - {r}")

    if errors or failed_requests or any(v == 0 for v in checks.values()):
        return 1
    return 0


if __name__ == "__main__":
    url = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:18899/?v=21"
    out = sys.argv[2] if len(sys.argv) > 2 else "tools/dev/screenshots/smoke.png"
    sys.exit(main(url, out))
