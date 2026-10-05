package goui

import "testing"

func TestNativeMenuCacheObservesEnabledAndKeepsPublishedMapsImmutable(t *testing.T) {
	p := &macNativePlatform{items: map[string]macMenuItem{"hide": {item: 1}, "minimize": {item: 2}}}
	values := map[int]macMenuState{1: {title: "Hide Water", key: "h", modifiers: 1 << 20, enabled: true}, 2: {title: "Minimize", key: "m", modifiers: 1 << 20, enabled: true}}
	staticReads, enabledReads := 0, 0
	read := func(ref macMenuItem, full bool) macMenuState {
		enabledReads++
		value := values[int(ref.item)]
		if full {
			staticReads++
			return value
		}
		return macMenuState{enabled: value.enabled}
	}
	first, changed := p.snapshotMenu(read)
	if !changed || staticReads != 2 || enabledReads != 2 {
		t.Fatal("initial menu did not read its properties")
	}
	_, changed = p.snapshotMenu(read)
	if changed || staticReads != 2 || enabledReads != 4 {
		t.Fatal("unchanged menu was rebuilt or dynamic enabled state was not checked")
	}
	if allocations := testing.AllocsPerRun(10, func() { p.snapshotMenu(read) }); allocations != 0 {
		t.Fatalf("unchanged menu allocated %g objects", allocations)
	}
	value := values[1]
	value.enabled = false
	values[1] = value
	second, changed := p.snapshotMenu(read)
	if !changed || second["hide"].(map[string]any)["enabled"] != false {
		t.Fatal("enabled change was not published immediately")
	}
	if first["hide"].(map[string]any)["enabled"] != true {
		t.Fatal("enabled update mutated an earlier snapshot")
	}
	value = values[1]
	value.title = "隐藏 Water"
	value.key = "w"
	values[1] = value
	p.menuDirty = true // Same invalidation used by configuration changes in Update.
	third, changed := p.snapshotMenu(read)
	if !changed || staticReads != 4 || third["hide"].(map[string]any)["key"] != "w" || third["hide"].(map[string]any)["title"] != "隐藏 Water" {
		t.Fatal("configuration-owned properties were not refreshed immediately")
	}
	if second["hide"].(map[string]any)["key"] != "h" || second["hide"].(map[string]any)["title"] != "Hide Water" {
		t.Fatal("new configuration mutated a retained menu snapshot")
	}
}
