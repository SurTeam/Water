"""Workspace closure through real browser controls, including fixed confirmation UUID."""
import json
from playwright.sync_api import expect


def verify_workspace_close(page, directory, ctl):
    original = page.locator('#workspaces').input_value()
    ctl('workspace', 'new')
    state = ctl('state')
    created = next(w for w in state['workspaces'] if w['id'] != original)
    ctl('tab', 'close', '--tab', created['tabs'][0]['id'])
    expect(page.locator('#workspaces option')).to_have_count(2)
    empty = page.locator('#workspaces option').nth(1).get_attribute('value')
    page.locator('#workspaces').select_option(empty)
    expect(page.locator('#panes option')).to_have_count(0)
    page.locator('#close-workspace').click()
    expect(page.locator('#workspaces option')).to_have_count(1)
    page.locator('#new-workspace').click()
    expect(page.locator('#workspaces option')).to_have_count(2)
    target = page.locator('#workspaces option').nth(1).get_attribute('value')
    page.locator('#workspaces').select_option(target)
    page.locator('#close-workspace').click()
    expect(page.locator('#close-workspace-dialog')).to_be_visible()
    page.locator('#close-workspace-cancel').click()
    expect(page.locator('#workspaces option')).to_have_count(2)
    page.keyboard.press('Control+Alt+w')
    expect(page.locator('#close-workspace-dialog')).to_be_visible()
    page.keyboard.press('Escape')
    expect(page.locator('#workspaces option')).to_have_count(2)
    page.locator('#settings-open').click()
    page.locator('#close-workspace-shortcut').fill('ctrl-alt-q')
    page.locator('#close-workspace-shortcut').dispatch_event('change')
    page.locator('#web-settings button').filter(has_text='Close').first.click()
    page.reload()
    expect(page.locator('#workspaces option')).to_have_count(2)
    page.locator('#workspaces').select_option(target)
    page.keyboard.press('Control+Alt+q')
    expect(page.locator('#close-workspace-dialog')).to_be_visible()
    # A snapshot/selection change while the modal is open must not retarget it.
    page.evaluate("value => {const s=document.getElementById('workspaces');s.value=value;s.dispatchEvent(new Event('change'));}", original)
    page.locator('#close-workspace-confirm').click()
    expect(page.locator('#workspaces option')).to_have_count(1)
    assert page.locator('#workspaces option').get_attribute('value') == original
    page.locator('#settings-open').click()
    page.locator('#close-workspace-reset').click()
    page.locator('#web-settings button').filter(has_text='Close').first.click()
    expect(page.locator('#status')).to_have_text('Connected')
    (directory / 'workspace-close-assertions.json').write_text(json.dumps({
        'empty_workspace_selectable_and_closes_without_confirmation': True,
        'populated_workspace_requires_confirmation': True,
        'cancel_and_escape_preserve_workspace': True,
        'default_and_persisted_custom_shortcuts': True,
        'confirmation_keeps_original_uuid': True}, indent=2))
