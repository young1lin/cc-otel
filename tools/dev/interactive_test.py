"""
Interactive smoke test: verify theme toggle, range tabs, panel switches, modals.
"""
import sys
from pathlib import Path
from playwright.sync_api import sync_playwright


def main(url: str, screenshot_dir: str) -> int:
    Path(screenshot_dir).mkdir(parents=True, exist_ok=True)
    errors: list[str] = []
    failed: list[str] = []

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        ctx = browser.new_context(viewport={"width": 1440, "height": 900})
        page = ctx.new_page()

        page.on("console", lambda msg: errors.append(f"[{msg.type}] {msg.text}") if msg.type == "error" else None)
        page.on("pageerror", lambda exc: errors.append(f"[pageerror] {exc}"))
        page.on("requestfailed", lambda req: failed.append(f"{req.method} {req.url}"))

        page.goto(url, wait_until="load", timeout=15000)
        page.wait_for_timeout(2500)
        page.screenshot(path=f"{screenshot_dir}/01_load.png", full_page=True)

        # Click 7-Days range tab
        page.locator('button.range-btn[data-range="week"]').click()
        page.wait_for_timeout(1500)
        page.screenshot(path=f"{screenshot_dir}/02_week.png", full_page=True)

        # Toggle theme
        page.locator("#theme-toggle").click()
        page.wait_for_timeout(1500)
        theme_after = page.evaluate('document.documentElement.getAttribute("data-theme")')
        page.screenshot(path=f"{screenshot_dir}/03_theme_toggled.png", full_page=True)

        # Toggle back
        page.locator("#theme-toggle").click()
        page.wait_for_timeout(1000)

        # Switch to Cost metric
        page.locator('button.metric-sw-btn[data-metric="cost"]').click()
        page.wait_for_timeout(1000)
        page.screenshot(path=f"{screenshot_dir}/04_cost_metric.png", full_page=True)

        # Click Total Cost KPI -> opens breakdown modal
        page.locator("#kpi-total-cost").click()
        page.wait_for_timeout(1500)
        modal_visible = page.locator("#cost-modal").evaluate("el => getComputedStyle(el).display !== 'none'")
        page.screenshot(path=f"{screenshot_dir}/05_breakdown_modal.png", full_page=True)
        # Close it
        page.locator("#cost-close").click()
        page.wait_for_timeout(500)

        # Click status indicator
        page.locator("#status-btn").click()
        page.wait_for_timeout(1000)
        status_visible = page.locator("#status-modal").evaluate("el => getComputedStyle(el).display !== 'none'")
        page.screenshot(path=f"{screenshot_dir}/06_status_modal.png", full_page=True)
        page.locator("#status-close").click()

        # Sessions panel
        page.locator('button.panel-btn[data-panel="sessions"]').click()
        page.wait_for_timeout(1500)
        page.screenshot(path=f"{screenshot_dir}/07_sessions.png", full_page=True)

        # Request Log panel
        page.locator('button.panel-btn[data-panel="requests"]').click()
        page.wait_for_timeout(1500)
        page.screenshot(path=f"{screenshot_dir}/08_requests.png", full_page=True)

        browser.close()

    print(f"theme after toggle: {theme_after}")
    print(f"breakdown modal opened: {modal_visible}")
    print(f"status modal opened: {status_visible}")
    print(f"console errors: {len(errors)}")
    for e in errors:
        print(f"  - {e}")
    print(f"failed requests: {len(failed)}")
    for f in failed:
        print(f"  - {f}")

    if errors or failed or theme_after != 'dark' or not modal_visible or not status_visible:
        return 1
    return 0


if __name__ == "__main__":
    url = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:18899/?v=23"
    out = sys.argv[2] if len(sys.argv) > 2 else "tools/dev/screenshots/interactive"
    sys.exit(main(url, out))
