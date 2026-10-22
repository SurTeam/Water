#!/usr/bin/env python3
"""Real Cmd+V regressions: CJK redraw, UTF-8 integrity and paste framing."""
import hashlib
import shlex
import subprocess
import sys

from water_test import WaterGUI

RECEIVER = '''import hashlib, os, termios, tty
size, mode, name = {size}, {mode!r}, {name!r}
sentinel = b'WATER_PASTE_END_7bc92d'
old = termios.tcgetattr(0)
data = bytearray()
try:
    tty.setraw(0)
    os.write(1, ('\\x1b[?2004' + mode + 'READY_' + name + '\\r\\n').encode())
    while len(data) < size + len(sentinel):
        chunk = os.read(0, min(4096, size + len(sentinel) - len(data)))
        if not chunk:
            raise RuntimeError('PTY closed before complete paste')
        data.extend(chunk)
        os.write(1, chunk)
finally:
    termios.tcsetattr(0, termios.TCSANOW, old)
    os.write(1, b'\\x1b[?2004l')
assert data.endswith(sentinel), 'extra or missing paste bytes before end marker'
del data[-len(sentinel):]
print('\\nRESULT_' + name + ':' + str(len(data)) + ':' + hashlib.sha256(data).hexdigest(), flush=True)
'''


def main():
    with WaterGUI('water-text-paste.') as test:
        pane = test.ctl('ui', 'snapshot')['frame_focused_pane']
        fixture = test.root / 'scripts/clipboard-fixture.swift'
        saved = test.directory / 'clipboard.json'
        subprocess.run(['swift', str(fixture), 'save', str(saved)], check=True, timeout=20)
        try:
            for bracketed in (False, True):
                for label, text, repeats in [('repeat', '啊啊啊啊啊啊啊', 80),
                                             ('large', '啊中文🙂abc' * 16384, 1)]:
                    name = label + ('-bracketed' if bracketed else '-plain')
                    payload = text.encode()
                    frame = b'\x1b[200~' + payload + b'\x1b[201~' if bracketed else payload
                    expected = frame * repeats
                    digest = hashlib.sha256(expected).hexdigest()
                    test.save(name + '-expected.json', {'payload_bytes': len(payload), 'repeats': repeats,
                                                       'wire_bytes': len(expected), 'sha256': digest})
                    source = test.save(name + '.py', RECEIVER.format(
                        size=len(expected), mode='h' if bracketed else 'l', name=name))
                    test.ctl('pane', 'input', '--pane', pane, '--text',
                             shlex.join([sys.executable, str(source)]) + '\n')
                    # The marker follows DECSET/DECRST in the same output stream,
                    # so observing it also proves the GUI has parsed the mode.
                    test.wait(lambda: 'READY_' + name in test.content(pane)['text'], 'receiver ' + name)
                    subprocess.run(['pbcopy'], input=payload, check=True, timeout=5)
                    for _ in range(repeats):
                        test.ctl('ui', 'key', 'cmd-v')
                    # A separately ordered marker catches duplicated trailing
                    # bytes rather than accepting only the expected prefix.
                    test.ctl('ui', 'key', 'text:WATER_PASTE_END_7bc92d')
                    marker = 'RESULT_' + name + ':' + str(len(expected)) + ':' + digest
                    test.wait(lambda: marker in test.content(pane)['text'].replace('\n', ''),
                              'complete paste ' + name, timeout=15)
                    test.save(name + '-content.json', test.content(pane))
                    test.screenshot(name + '.png')
                    print('PASS ' + marker, flush=True)

            # ZLE redraw/control sequences differ from raw echo. Verify the
            # actual edited command using its digest, then draw each long line.
            repeats = 150
            for cycle in range(3):
                text = '啊啊啊啊啊啊啊周期' + str(cycle)
                digest = hashlib.sha256((text * repeats).encode()).hexdigest()
                subprocess.run(['pbcopy'], input=text.encode(), check=True, timeout=5)
                before = test.content(pane)['last_seq']
                test.ctl('ui', 'key', "text:printf '%s' '")
                for _ in range(repeats):
                    test.ctl('ui', 'key', 'cmd-v')
                test.wait(lambda: (lambda c: c['last_seq'] > before and text in c['text'])(test.content(pane)),
                          'Chinese ZLE redraw')
                test.screenshot('zle-' + str(cycle) + '.png')
                test.ctl('ui', 'key', "text:' | shasum -a 256; printf '\\nZLE_%s_%s\\n' DONE " + str(cycle))
                test.ctl('ui', 'key', 'Return')
                test.wait(lambda: digest in test.content(pane)['text'].replace('\n', '')
                          and 'ZLE_DONE_' + str(cycle) in test.content(pane)['text'],
                          'ZLE command integrity ' + str(cycle))
            print('PASS 450 Chinese Cmd+V events through ZLE; distinct SHA-256 per cycle', flush=True)
        finally:
            subprocess.run(['swift', str(fixture), 'restore', str(saved)], check=True, timeout=20)
            saved.unlink()


if __name__ == '__main__':
    main()
