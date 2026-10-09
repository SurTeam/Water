"""Visual viewport keyboard geometry fixture; this does not emulate a physical IME."""
import json
from playwright.sync_api import expect


def install_visual_viewport_fixture(context):
    context.add_init_script("""(() => {
      const native = window.visualViewport, state = {height: null, top: null};
      const viewport = new EventTarget();
      Object.defineProperties(viewport, {
        height: {get: () => state.height ?? native.height},
        offsetTop: {get: () => state.top ?? native.offsetTop},
        width: {get: () => native.width}, scale: {get: () => native.scale}
      });
      for (const type of ['resize', 'scroll']) native.addEventListener(type, () => viewport.dispatchEvent(new Event(type)));
      Object.defineProperty(window, 'visualViewport', {configurable: true, value: viewport});
      window.__keyboardViewport = {set(height, top, type) {
        state.height = height; state.top = top; viewport.dispatchEvent(new Event(type));
      }};
    })();""")


def verify_keyboard_helpers(page, directory, kind):
    metrics = """() => {
      const body = document.body.getBoundingClientRect(),
        footer = document.getElementById('keyboard-helpers').getBoundingClientRect(),
        terminal = document.getElementById('terminal').getBoundingClientRect();
      return {bodyTop: body.top, bodyHeight: body.height, footerTop: footer.top,
        footerBottom: footer.bottom, terminalHeight: terminal.height,
        viewportHeight: visualViewport.height, viewportTop: visualViewport.offsetTop,
        layoutHeight: innerHeight};
    }"""
    visible = """() => {
      const body = document.body.getBoundingClientRect(),
        footer = document.getElementById('keyboard-helpers').getBoundingClientRect(),
        terminal = document.getElementById('terminal').getBoundingClientRect();
      return Math.abs(body.height - visualViewport.height) < 1 &&
        Math.abs(body.top - visualViewport.offsetTop) < 1 &&
        footer.bottom <= visualViewport.offsetTop + visualViewport.height + 1 &&
        terminal.bottom <= footer.top + 1 && terminal.height > 0;
    }"""
    page.locator('.xterm-helper-textarea').focus()
    before = page.evaluate(metrics)
    page.evaluate("window.__keyboardViewport.set(480, 0, 'resize')")
    page.wait_for_function(visible, timeout=5000)
    shrunk = page.evaluate(metrics)
    assert shrunk['layoutHeight'] == before['layoutHeight'], 'layout viewport changed; keyboard-only case not exercised'
    assert shrunk['terminalHeight'] < before['terminalHeight'] - 250, 'terminal did not make space for the keyboard'
    page.evaluate("window.__keyboardViewport.set(480, 72, 'scroll')")
    page.wait_for_function(visible, timeout=5000)
    panned = page.evaluate(metrics)
    page.get_by_role('button', name='Hide keyboard', exact=True).click()
    assert not page.evaluate("document.activeElement.matches('.xterm-helper-textarea')"), 'hide keyboard kept terminal input focused'
    page.locator('#settings-open').click()
    page.locator('#web-settings').get_by_role('button', name='Close', exact=True).click()
    assert not page.evaluate("document.activeElement.matches('.xterm-helper-textarea')"), 'closing settings reopened a dismissed keyboard'
    page.get_by_role('button', name='Tab', exact=True).click()
    assert not page.evaluate("document.activeElement.matches('.xterm-helper-textarea')"), 'helper reopened a dismissed keyboard'
    page.get_by_role('button', name='Show keyboard', exact=True).click()
    assert page.evaluate("document.activeElement.matches('.xterm-helper-textarea')"), 'show keyboard failed to focus input'
    # Clear the tab-completion input before the next terminal assertion.
    page.keyboard.press('Control+C')
    page.get_by_role('button', name='Ctrl', exact=True).click()
    assert page.evaluate("document.activeElement.matches('.xterm-helper-textarea')"), 'Ctrl dismissed terminal focus'
    page.get_by_role('button', name='Ctrl', exact=True).click()
    page.keyboard.type("printf 'KEYBOARD_%s\\n' FLOW_OK", delay=1)
    page.get_by_role('button', name='Enter', exact=True).click()
    expect(page.locator('.xterm-accessibility-tree')).to_contain_text('KEYBOARD_FLOW_OK', timeout=5000)
    assert page.evaluate("document.activeElement.matches('.xterm-helper-textarea')"), 'helper dismissed terminal focus'
    page.evaluate("""() => {
      const keyboard = document.createElement('div'); keyboard.id = 'test-keyboard';
      keyboard.textContent = 'Simulated keyboard';
      keyboard.style.cssText = `position:fixed;top:${visualViewport.offsetTop+visualViewport.height}px;left:0;right:0;bottom:0;background:#45454b;color:white;display:grid;place-items:center;z-index:1000`;
      document.documentElement.append(keyboard);
    }""")
    page.screenshot(path=str(directory / (kind + '-keyboard.png')))
    page.evaluate("document.getElementById('test-keyboard').remove();window.__keyboardViewport.set(null,null,'resize')")
    page.wait_for_function(visible, timeout=5000)
    restored = page.evaluate(metrics)
    assert abs(restored['terminalHeight'] - before['terminalHeight']) < 2, 'terminal did not restore after keyboard close'
    page.set_viewport_size({'width': 844, 'height': 390})
    page.wait_for_function(visible, timeout=5000)
    landscape = page.evaluate(metrics)
    assert abs(landscape['bodyHeight'] - 390) < 1
    page.set_viewport_size({'width': 390, 'height': 844})
    page.wait_for_function(visible, timeout=5000)
    (directory / (kind + '-keyboard-assertions.json')).write_text(json.dumps({
        'before': before, 'keyboard_open': shrunk, 'viewport_panned': panned,
        'restored': restored, 'landscape': landscape, 'helper_input_roundtrip': True,
        'keyboard_hide_show': True, 'helpers_do_not_reopen_hidden_keyboard': True,
        'terminal_focus_preserved': True, 'note': 'VisualViewport events simulated; no physical phone IME'
    }, indent=2))
