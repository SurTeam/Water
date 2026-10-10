#!/usr/bin/env python3
import os
"""Verify real Pi redraw/history and input following without selecting or copying."""
import json
import shlex
import subprocess
import uuid

from water_test import WaterGUI

os.environ["WATER_TEST_UNFOCUSED"] = "1"


with WaterGUI("water-pi-redraw.", {"startup": {"window_columns": 90, "window_rows": 24}}) as test:
    ctl, wait = test.ctl, test.wait
    directory = test.directory
    pane = ctl("ui", "snapshot")["frame_focused_pane"]
    content = lambda: test.content(pane)
    ctl("pane", "input", "--pane", pane, "--text", "printf 'WATER_PREFIX_%03d\\n' {1..45}\n")
    wait(lambda: "WATER_PREFIX_045" in content()["text"], "shell history")

    # Replay a saved tool result through the installed Pi renderer; no model call.
    now = "2026-10-06T00:00:00.000Z"
    entries = [{"type": "session", "version": 3, "id": str(uuid.uuid4()), "timestamp": now, "cwd": str(directory)}]
    parent = None
    messages = [
        {"role": "user", "content": "Inspect the saved fixture", "timestamp": 1791244800000},
        {"role": "assistant", "content": [{"type": "toolCall", "id": "fixture-tool", "name": "bash", "arguments": {"command": "printf fixture"}}], "api": "openai-responses", "provider": "openai", "model": "gpt-5", "usage": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 0, "cost": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}}, "stopReason": "toolUse", "timestamp": 1791244800000},
        {"role": "toolResult", "toolCallId": "fixture-tool", "toolName": "bash", "content": [{"type": "text", "text": "\n".join(f"PI_DETAIL_{n:03d}" for n in range(80))}], "isError": False, "timestamp": 1791244800000},
    ]
    for message in messages:
        entry_id = uuid.uuid4().hex[:8]
        entries.append({"type": "message", "id": entry_id, "parentId": parent, "timestamp": now, "message": message})
        parent = entry_id
    session = test.save("fixture.jsonl", "".join(json.dumps(e) + "\n" for e in entries))
    resolved = subprocess.run([test.shell, "-c", 'eval "$(fnm env --shell zsh)"\ncommand -v pi'],
                              capture_output=True, text=True, check=True, timeout=5).stdout.strip()
    agent_dir = directory / "agent"
    agent_dir.mkdir()
    (agent_dir / "settings.json").write_text(json.dumps({"hideThinkingBlock": True, "showImages": False}))
    command = ('eval "$(fnm env --shell zsh)"\nPI_CODING_AGENT_DIR=' + shlex.quote(str(agent_dir))
               + " " + shlex.quote(resolved) + " --tui-mode regular --no-extensions --no-skills"
               + " --no-prompt-templates --session " + shlex.quote(str(session)) + "\n")
    ctl("pane", "input", "--pane", pane, "--text", command)
    wait(lambda: any(a["kind"] == "pi" for a in ctl("state").get("agents", [])), "Pi detected")
    wait(lambda: "PI_DETAIL_079" in content()["text"] and not content()["synchronized_output"], "Pi transcript rendered")

    def history(name):
        before = content()
        text = test.history(pane)
        after = content()
        assert after["y_disp"] == before["y_disp"], "content query changed scroll position"
        assert after["selection"] == before["selection"], "content query changed selection"
        assert not after["selection"]["Active"], "test must not leave a selected terminal"
        test.save(name + ".txt", text)
        return text

    initial = history("collapsed")
    welcome = "Pi can explain its own features"
    assert "WATER_PREFIX_040" in initial, "Pi startup lost shell history"
    assert "PI_DETAIL_000" not in initial, "fixture must start collapsed"
    assert initial.count(welcome) == 1, "startup welcome duplicated or missing"
    test.screenshot("collapsed.png")
    collapsed_rows = content()["total_rows"]
    for cycle in range(2):
        previous_seq = content()["last_seq"]
        ctl("ui", "key", "ctrl-o")
        wait(lambda: (lambda c: c["last_seq"] > previous_seq and not c["synchronized_output"]
                      and c["total_rows"] > collapsed_rows + 70)(content()), "expanded tools across viewport")
        expanded = history(f"expanded-{cycle}")
        for n in range(80):
            assert expanded.count(f"PI_DETAIL_{n:03d}") == 1, "expanded tool content duplicated or missing"
        assert expanded.count(welcome) == 1 and "WATER_PREFIX_040" in expanded
        test.screenshot(f"expanded-{cycle}.png")
        expanded_rows = content()["total_rows"]
        previous_seq = content()["last_seq"]
        ctl("ui", "key", "ctrl-o")
        wait(lambda: (lambda c: c["last_seq"] > previous_seq and not c["synchronized_output"]
                      and c["total_rows"] < expanded_rows - 60)(content()), "tools collapsed")
        collapsed = history(f"collapsed-{cycle}")
        assert "PI_DETAIL_000" not in collapsed, "collapsed view retained expanded history"
        assert collapsed.count("PI_DETAIL_079") == 1, "collapsed tail duplicated"
        assert collapsed.count(welcome) == 1 and "WATER_PREFIX_040" in collapsed
    test.screenshot("recollapsed.png")

    ctl("terminal", "spawn", "--pane", pane, "--program", test.shell, "--", "-f")
    wait(lambda: not ctl("state").get("agents"), "shell restored")
    ctl("pane", "input", "--pane", pane, "--text", "printf 'FOLLOW_HISTORY_%03d\\n' {1..80}\n")
    wait(lambda: "FOLLOW_HISTORY_080" in content()["text"], "follow fixture ready")
    grid = next(g for g in ctl("ui", "snapshot")["terminal_grids"] if g["pane_id"] == pane)
    x0, y0, x1, y1 = grid["rect"]
    ctl("ui", "wheel", "--x", (x0+x1)/2, "--y", (y0+y1)/2, "--dy", -200)
    wait(lambda: content()["y_disp"] < content()["y_base"], "scrolled history")
    ctl("ui", "key", "text:echo WATER_INPUT_FOLLOW_OK")
    wait(lambda: content()["y_disp"] == content()["y_base"], "typing immediately follows bottom")
    before_seq = content()["last_seq"]
    ctl("ui", "key", "enter")
    wait(lambda: (lambda c: c["last_seq"] > before_seq and "WATER_INPUT_FOLLOW_OK" in c["text"]
                  and c["y_disp"] == c["y_base"])(content()), "Enter output follows bottom")
    print("PASS real Pi Ctrl+O twice, retained history, single welcome, read-only GUI content without selection/clipboard, keyboard/Enter follow; artifacts=" + str(directory), flush=True)
