"""Browser font preference behavior against the embedded, live Water page."""
import json
import re
from playwright.sync_api import expect


def verify_web_fonts(page, directory, kind, marker):
    errors = []
    requests = []
    page.on("request", lambda request: requests.append(request.url))
    page.locator('.xterm-helper-textarea').focus()
    page.locator("#settings-open").click()
    dialog = page.locator("#web-settings")
    expect(dialog).to_be_visible(timeout=5000)
    options = page.locator("#font-family option").evaluate_all("nodes => nodes.map(n => ({id:n.value,label:n.textContent}))")
    assert {"default", "monospace"}.issubset({o["id"] for o in options})
    choice = next((o["id"] for o in options if o["id"] not in ("default", "monospace")), "monospace")
    expected = choice if choice != "monospace" else "ui-monospace"
    page.locator("#font-family").select_option(choice)
    page.wait_for_function("font => JSON.parse(localStorage.getItem('water.web.settings')).font === font", arg=choice, timeout=5000)
    expect(page.locator("html")).to_have_css("font-family", re.compile(re.escape(expected)), timeout=5000)
    expect(page.locator(".xterm-accessibility-tree")).to_have_css("font-family", re.compile(re.escape(expected)), timeout=5000)
    bounds = dialog.bounding_box()
    assert bounds and bounds["x"] >= 0 and bounds["y"] >= 0
    assert bounds["x"] + bounds["width"] <= page.viewport_size["width"] + 1
    assert bounds["y"] + bounds["height"] <= page.viewport_size["height"] + 1
    page.screenshot(path=str(directory / (kind + "-font-settings.png")))
    dialog.get_by_role("button", name="Close", exact=True).click()
    expect(dialog).to_be_hidden()
    expect(page.locator(".xterm-helper-textarea")).to_be_focused(timeout=5000)
    page.keyboard.type("printf 'FONT_%s\\n' SETTINGS_OK", delay=1)
    page.keyboard.press("Enter")
    expect(page.locator(".xterm-accessibility-tree")).to_contain_text("FONT_SETTINGS_OK", timeout=5000)
    page.reload(timeout=10000)
    expect(page.locator(".xterm-accessibility-tree")).to_contain_text(marker, timeout=5000)
    expect(page.locator("#font-family")).to_have_value(choice)
    expect(page.locator(".xterm-accessibility-tree")).to_have_css("font-family", re.compile(re.escape(expected)), timeout=5000)
    page.locator("#reconnect").click()
    expect(page.locator(".xterm-accessibility-tree")).to_contain_text("FONT_SETTINGS_OK", timeout=5000)
    expect(page.locator(".xterm-accessibility-tree")).to_have_css("font-family", re.compile(re.escape(expected)), timeout=5000)
    other = page.context.new_page()
    try:
        other.goto(page.url, timeout=10000)
        expect(other.locator("#font-family")).to_have_value(choice)
        expect(other.locator(".xterm-accessibility-tree")).to_have_css("font-family", re.compile(re.escape(expected)), timeout=5000)
    finally:
        other.close()
        page.bring_to_front()
    # Separate unauthorized contexts verify storage failure and invalid values
    # without touching the paired browser's cookies or saved preference.
    for mode in ("blocked", "invalid"):
        context = page.context.browser.new_context(ignore_https_errors=True, viewport={"width": 390, "height": 844})
        try:
            if mode == "blocked":
                context.add_init_script("Object.defineProperty(window,'localStorage',{get(){throw new DOMException('Blocked','SecurityError')}})")
            probe = context.new_page()
            probe.on("pageerror", lambda error: errors.append(str(error)))
            probe.goto(page.url, timeout=10000)
            expect(probe.locator("#status")).to_contain_text("Not paired", timeout=5000)
            if mode == "invalid":
                probe.evaluate("localStorage.setItem('water.web.settings',JSON.stringify({version:1,font:'missing-system-font'}))")
                probe.reload(timeout=10000)
                expect(probe.locator("#font-family")).to_have_value("default")
                probe.evaluate("localStorage.setItem('water.web.settings','{invalid-json')")
                probe.reload(timeout=10000)
                expect(probe.locator("#font-family")).to_have_value("default")
            probe.locator("#settings-open").click()
            probe.locator("#font-family").select_option("monospace")
            expect(probe.locator("html")).to_have_css("font-family", re.compile("ui-monospace"))
            if mode == "blocked":
                expect(probe.locator("#settings-note")).to_contain_text("this visit only")
            probe.keyboard.press("Escape")
            expect(probe.locator("#web-settings")).to_be_hidden()
        finally:
            context.close()
    assert not errors, errors
    assert not any(re.search(r"\.(woff2?|ttf|otf)(?:[?#]|$)", url) for url in requests), requests
    page.bring_to_front()
    page.locator('.xterm-helper-textarea').focus()
    page.wait_for_function("() => document.hasFocus() && !document.hidden", timeout=5000)
    (directory / (kind + '-font-geometry.json')).write_text(json.dumps(page.evaluate("() => ({focused:document.hasFocus(),hidden:document.hidden,terminal:document.getElementById('terminal').getBoundingClientRect().toJSON(),screen:document.querySelector('.xterm-screen').getBoundingClientRect().toJSON()})"),indent=2))
    page.wait_for_function("() => {const t=document.querySelector('.xterm-screen').getBoundingClientRect(),m=document.getElementById('terminal').getBoundingClientRect();return t.width>0 && t.width<=m.width+1 && t.height<=m.height+1;}", timeout=5000)
    (directory / (kind + "-font-assertions.json")).write_text(json.dumps({
        "available_fonts": options, "selected": choice, "saved": True,
        "restored_after_reload": True, "restored_after_reconnect": True,
        "shared_with_new_tab": True, "terminal_input": True,
        "blocked_storage_fallback": True, "invalid_storage_fallback": True,
        "dialog_fits_mobile_viewport": True, "no_font_downloads": True,
        "terminal_fits": True, "browser_errors": errors
    }, indent=2))
