#!/usr/bin/env python3
"""Exercise Agent profile editing, recognition, OSC mapping and controls in Draw."""
import json
import shlex
from PIL import Image
from water_test import WaterGUI


def snapshot(test):
    return test.ctl("ui", "snapshot")


def ready(test):
    return test.wait(lambda: (lambda s: s if s.get("agent_settings", {}).get("loaded")
                             and not s["agent_settings"]["busy"] else None)(snapshot(test)), "Agent panel ready")


def click(test, label):
    def shown():
        state = snapshot(test)
        if any(h.get("label") == label for h in state["automation_hits"]):
            return state
        order = ["Name", "Enabled", "Priority", "Color", "Rules", "StatusSource", "OSC", "Interrupt", "Exit", "PreserveClear", "LongRunSeconds"]
        def field_name(value):
            for prefix in ("field:Agents.", "agent:cycle:"):
                if value.startswith(prefix):
                    return value[len(prefix):]
            return None
        target = field_name(label)
        visible = [order.index(name) for h in state["automation_hits"]
                   if (name := field_name(h.get("label", ""))) in order]
        if target in order and visible:
            width, height = state["frame_size"]
            direction = -88 if order.index(target) < min(visible) else 88
            test.ctl("ui", "wheel", "--x", width*.7, "--y", height*.5, "--dy", direction)
        return None
    state = test.wait(shown, label)
    hit = next(h for h in state["automation_hits"] if h.get("label") == label)
    x0, y0, x1, y1 = hit["rect"]
    assert test.ctl("ui", "click", "--x", (x0+x1)/2, "--y", (y0+y1)/2)["handled"]


def select(test, agent_id):
    for _ in range(len(test.ctl("agent", "list")["profiles"]) + 1):
        if ready(test)["agent_settings"]["selected"] == agent_id:
            return
        click(test, "agent:next")
    raise AssertionError("Agent profile cannot be selected: " + agent_id)


def edit(test, field, value):
    click(test, "field:Agents." + field)
    test.ctl("ui", "key", "cmd-a")
    test.ctl("ui", "key", "text:" + value)
    test.wait(lambda: snapshot(test).get("settings_editor", {}).get("text") == value, "edit " + field)


def save(test):
    click(test, "agent:save")
    state = ready(test)
    assert state["agent_settings"]["message"] == "Agent settings saved", state["agent_settings"]


def agents(test):
    return snapshot(test)["sidebar_agents"]


with WaterGUI("water-agent-settings.", config={"startup": {"window_columns": 100, "window_rows": 28},
                                               "ui": {"system_notifications": False}}) as test:
    pane = snapshot(test)["frame_focused_pane"]
    test.ctl("ui", "key", "cmd-,")
    click(test, "category:Agents")
    ready(test)
    click(test, "agent:new")
    edit(test, "Name", "Fixture Agent")
    edit(test, "Color", "#12ab34")
    edit(test, "Rules", json.dumps([{"all": [{"field": "executable", "op": "exact", "value": "water-fixture-agent"}]}], separators=(",", ":")))
    edit(test, "OSC", "Idle,Running,Error,Running,Running")
    edit(test, "LongRunSeconds", "1")
    click(test, "agent:cycle:Interrupt")  # Ctrl+C -> SIGINT
    save(test)
    definitions = test.ctl("agent", "list")
    profile = definitions["config"]["custom"][0]
    agent_id = profile["id"]
    assert profile["interrupt"] == "sigint" and profile["name"] == "Fixture Agent"
    probe = "trap 'exit 0' INT TERM; printf '\\033]9;4;4\\007'; while IFS= read -r line; do printf '\\033]9;4;%s\\007' \"$line\"; done"
    command = "exec -a water-fixture-agent " + shlex.quote(test.shell) + " -f -c " + shlex.quote(probe) + "\n"
    test.ctl("pane", "input", "--pane", pane, "--text", command)
    test.wait(lambda: any(a["pane_id"] == pane and a["title"] == "Fixture Agent" and a["status"] == "Running"
                          for a in agents(test)), "custom recognition and OSC 4 mapping")
    click(test, "agent:test")
    diagnosed = ready(test)["agent_settings"]["diagnostic"]
    assert diagnosed["selected"]["id"] == agent_id and len(diagnosed["matches"]) == 1
    assert diagnosed["process"]["pid"] > 1 and diagnosed["process"]["started"]
    # Verify actual configured color in the status icon, through a Draw screenshot.
    test.ctl("ui", "key", "Escape")
    test.wait(lambda: "agent_settings" not in snapshot(test), "close clean panel for pixels")
    test.wait(lambda: any(h["kind"] == "tab" and h["label"] == "Fixture Agent" for h in snapshot(test)["automation_hits"]), "automatic tab uses Agent display name")
    state = snapshot(test)
    hit = next(h for h in state["automation_hits"] if h["kind"] == "agent" and h["id"] == pane)
    x0, y0, x1, y1 = hit["rect"]
    scale = state["display_scale"]
    pad = min(round(state["effective_config"]["ui"]["sidebar_agent_row_padding"]*scale), (x1-x0)//8)
    pixels = Image.open(test.screenshot("configured-agent.png")).convert("RGB").crop((x0+pad, y0, x0+pad+round(16*scale), y1))
    assert any(max(abs(a-b) for a, b in zip(rgb, (18, 171, 52))) < 8 for rgb in pixels.get_flattened_data()), "configured color not rendered"
    test.ctl("ui", "key", "cmd-,")
    click(test, "category:Agents")
    select(test, agent_id)
    edit(test, "OSC", "Idle,Running,Error,Running,Paused")
    save(test)
    test.wait(lambda: any(a["pane_id"] == pane and a["status"] == "Paused" for a in agents(test)), "OSC mapping hot update")
    click(test, "agent:cycle:StatusSource")  # auto -> osc
    click(test, "agent:cycle:StatusSource")  # osc -> process
    save(test)
    test.wait(lambda: any(a["pane_id"] == pane and a["status"] == "Running" for a in agents(test)), "process source ignores OSC pause")
    click(test, "agent:cycle:StatusSource")  # process -> auto
    save(test)
    test.wait(lambda: any(a["pane_id"] == pane and a["status"] == "Paused" for a in agents(test)), "automatic source restored OSC mapping")
    click(test, "field:Agents.Enabled")
    save(test)
    test.wait(lambda: not agents(test), "disabled Agent reclassified")
    click(test, "field:Agents.Enabled")
    save(test)
    test.wait(lambda: any(a["pane_id"] == pane for a in agents(test)), "reenabled Agent")
    click(test, "agent:interrupt")
    state = ready(test)
    assert state["agent_settings"]["message"] == "Agent action completed", state["agent_settings"]
    test.wait(lambda: not any(a["pane_id"] == pane for a in agents(test)), "SIGINT ended owned fixture")
    # Builtin overrides and restore are edited through the same panel.
    select(test, "codex")
    edit(test, "Name", "My Codex")
    save(test)
    assert any(o["id"] == "codex" and o["name"] == "My Codex" for o in test.ctl("agent", "list")["config"]["overrides"])
    click(test, "agent:restore")
    save(test)
    assert not any(o["id"] == "codex" and "name" in o for o in test.ctl("agent", "list")["config"].get("overrides", []))
    # Cycling reaches a custom profile even when it is on a different list page.
    select(test, agent_id)
    click(test, "agent:duplicate")
    save(test)
    assert len(test.ctl("agent", "list")["config"]["custom"]) == 2
    click(test, "agent:delete")
    save(test)
    assert len(test.ctl("agent", "list")["config"]["custom"]) == 1
    click(test, "agent:new")
    assert ready(test)["agent_settings"]["dirty"]
    click(test, "agent:close")
    assert "agent_settings" in snapshot(test), "dirty panel closed without explicit discard"
    click(test, "agent:close")
    test.wait(lambda: "agent_settings" not in snapshot(test), "discard closed panel")
    assert len(test.ctl("agent", "list")["config"]["custom"]) == 1
    saved = json.loads((test.directory / "config.json").read_text())
    assert saved["agents"]["custom"][0]["id"] == agent_id
    exported = test.ctl("agent", "export")
    export_file = test.save("agents-export.json", exported)
    imported = test.ctl("agent", "configure", "--file", export_file)
    assert imported["config"]["custom"][0]["id"] == agent_id
    launch = "exec -a water-fixture-agent " + shlex.quote(test.shell) + " -f -c " + shlex.quote(probe)
    spawned = test.ctl("terminal", "spawn", "--program", test.shell, "--", "-f", "-c", launch)
    terminal = spawned["result"]["terminal_id"]
    test.wait(lambda: any(a["title"] == "Fixture Agent" for a in agents(test)), "CLI probe recognized")
    checked = test.ctl("agent", "test", "--terminal", terminal, "--file", export_file)
    assert checked["selected"]["id"] == agent_id
    stopped = test.ctl("agent", "exit", "--terminal", terminal)
    assert stopped["preset"] == "sigterm" and stopped["terminal_id"] == terminal
    test.wait(lambda: not agents(test), "CLI SIGTERM ended probe")
    test.save("diagnostic.json", diagnosed)
    test.save("definitions.json", test.ctl("agent", "list"))
    print("PASS: Agent GUI CRUD/restore/discard, recognition, OSC mapping, color and SIGINT; artifacts=" + str(test.directory), flush=True)
