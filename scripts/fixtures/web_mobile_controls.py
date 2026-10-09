"""Compact controls and optional private photo regression; never redeems its invite."""
import json
import os
from urllib.parse import urlparse
from playwright.sync_api import expect


def verify_mobile_controls(page, directory):
    before = page.locator('#terminal').bounding_box()
    selected = page.locator('#panes').input_value()
    page.locator('.xterm-helper-textarea').focus()
    page.get_by_role('button', name='Collapse controls', exact=True).click()
    expect(page.locator('#top-controls')).to_be_hidden()
    expect(page.get_by_role('button', name='Expand controls', exact=True)).to_have_attribute('aria-expanded', 'false')
    page.wait_for_function("() => document.getElementById('terminal').getBoundingClientRect().top <= document.querySelector('header').getBoundingClientRect().bottom+1", timeout=5000)
    after = page.locator('#terminal').bounding_box()
    assert after['height'] > before['height'] + 100, (before, after)
    assert page.locator('header').bounding_box()['height'] <= 64, 'collapsed title is not a single row'
    assert page.locator('#panes').input_value() == selected
    assert page.evaluate("document.activeElement.matches('.xterm-helper-textarea')"), 'collapse lost terminal focus'
    page.keyboard.type("printf 'COLLAPSED_%s\\n' INPUT_OK", delay=1)
    page.keyboard.press('Enter')
    expect(page.locator('.xterm-accessibility-tree')).to_contain_text('COLLAPSED_INPUT_OK', timeout=5000)
    page.screenshot(path=str(directory / 'controls-collapsed.png'))
    page.reload(timeout=10000)
    expect(page.locator('#status')).to_have_text('Connected', timeout=10000)
    expect(page.locator('#top-controls')).to_be_hidden()
    page.get_by_role('button', name='Expand controls', exact=True).click()
    expect(page.locator('#top-controls')).to_be_visible()
    expect(page.locator('#server-controls')).to_be_visible()
    expect(page.locator('#terminal-controls')).to_be_visible()
    page.screenshot(path=str(directory / 'controls-expanded.png'))
    (directory / 'controls-assertions.json').write_text(json.dumps({
        'single_title_row': True, 'terminal_height_before': before['height'],
        'terminal_height_after': after['height'], 'selection_preserved': True,
        'input_while_collapsed': True, 'reload_preserves_collapsed': True,
        'expand_restores_controls': True}, indent=2))

    source = os.environ.get('WATER_QR_PHOTO')
    if not source:
        return
    photo_page = page.context.new_page()
    origin = urlparse(page.url)
    blocked = []
    def intercept(route):
        request = route.request
        target = urlparse(request.url)
        if request.is_navigation_request() and (target.scheme, target.netloc) != (origin.scheme, origin.netloc):
            blocked.append({'scheme': target.scheme, 'host': target.hostname, 'port': target.port})
            route.abort()  # Never navigate to or redeem the user's invitation.
        else:
            route.continue_()
    photo_page.route('**/*', intercept)
    try:
        photo_page.goto(page.url, timeout=10000)
        expect(photo_page.locator('#status')).to_have_text('Connected', timeout=10000)
        with photo_page.expect_file_chooser() as chooser:
            photo_page.locator('#scan-qr').click()
        with photo_page.expect_event('requestfailed', predicate=lambda r: r.is_navigation_request() and urlparse(r.url).netloc != origin.netloc, timeout=20000):
            chooser.value.set_files(source)
        assert blocked, 'photo produced no structurally valid pairing navigation'
        assert all(value['scheme'] in ('http', 'https') for value in blocked)
        (directory / 'supplied-photo-assertions.json').write_text(json.dumps({
            'original_photo_decoded_in_real_browser': True, 'no_manual_crop': True,
            'photo_upload': False, 'invitation_redeemed': False,
            'external_navigation_blocked': True}, indent=2))
    finally:
        photo_page.close()
        page.bring_to_front()
