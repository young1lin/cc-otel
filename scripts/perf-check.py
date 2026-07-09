"""
Perf check for cc-otel Web UI.
Open dashboard, click 7D and 30D ranges, capture /api/* timings + any console errors.
"""
import json
import time
from playwright.sync_api import sync_playwright

BASE = "http://localhost:8899"


def main():
    timings = []  # list of (url, status, duration_ms)
    console_errors = []

    def on_response(response):
        url = response.url
        if "/api/" not in url:
            return
        req = response.request
        timing = response.request.timing
        # timing fields: requestStart, responseStart, responseEnd (ms since navigationStart)
        duration_ms = timing["responseEnd"] - timing["requestStart"]
        timings.append(
            {
                "url": url.replace(BASE, ""),
                "status": response.status,
                "duration_ms": round(duration_ms, 1),
                "method": req.method,
            }
        )

    def on_console(msg):
        if msg.type in ("error", "warning"):
            console_errors.append(f"[{msg.type}] {msg.text}")

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        ctx = browser.new_context()
        page = ctx.new_page()
        page.on("response", on_response)
        page.on("console", on_console)

        # 1) Cold load
        t0 = time.time()
        page.goto(f"{BASE}/?v={int(time.time())}", wait_until="load")
        cold_load_ms = (time.time() - t0) * 1000
        print(f"=== Cold load (today, default): {cold_load_ms:.0f} ms ===")
        page.screenshot(path="/tmp/cc-otel-today.png", full_page=True)

        # Snapshot timings then clear
        today_timings = list(timings)
        timings.clear()

        # 2) Click 7 Days
        # Look for the range selector button
        print("\n=== Clicking 7 Days ===")
        # Try common selector patterns
        try:
            page.get_by_text("7 Days", exact=False).first.click(timeout=5000)
        except Exception as e:
            print(f"  couldn't find '7 Days' button: {e}")
            # Print all visible text buttons for diagnostics
            buttons = page.locator("button").all()
            print(f"  visible buttons: {[b.inner_text()[:30] for b in buttons if b.is_visible()]}")
            browser.close()
            return
        t0 = time.time()
        page.wait_for_timeout(2500)
        seven_d_ms = (time.time() - t0) * 1000
        print(f"  7 Days networkidle: {seven_d_ms:.0f} ms")
        page.screenshot(path="/tmp/cc-otel-7d.png", full_page=True)
        seven_d_timings = list(timings)
        timings.clear()

        # 3) Click 30 Days
        print("\n=== Clicking 30 Days ===")
        try:
            page.get_by_text("30 Days", exact=False).first.click(timeout=5000)
        except Exception as e:
            print(f"  couldn't find '30 Days' button: {e}")
        t0 = time.time()
        page.wait_for_timeout(2500)
        thirty_d_ms = (time.time() - t0) * 1000
        print(f"  30 Days networkidle: {thirty_d_ms:.0f} ms")
        page.screenshot(path="/tmp/cc-otel-30d.png", full_page=True)
        thirty_d_timings = list(timings)
        timings.clear()

        browser.close()

    print("\n\n======== API timings ========")
    for label, t in [("TODAY (cold)", today_timings), ("7 DAYS", seven_d_timings), ("30 DAYS", thirty_d_timings)]:
        print(f"\n--- {label} ---")
        # sort by duration desc
        for item in sorted(t, key=lambda x: -x["duration_ms"]):
            print(f"  {item['duration_ms']:>7.1f} ms  [{item['status']}]  {item['url']}")

    if console_errors:
        print("\n\n======== Console errors/warnings ========")
        for e in console_errors[:30]:
            print(f"  {e}")


if __name__ == "__main__":
    main()
