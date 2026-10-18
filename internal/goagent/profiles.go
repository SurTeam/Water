package goagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

// Rules are ORed; the conditions within a rule are ANDed.
type Condition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}
type Rule struct {
	All []Condition `json:"all"`
}
type Profile struct {
	ID                      Kind   `json:"id"`
	Name                    string `json:"name"`
	Enabled                 bool   `json:"enabled"`
	Priority                int    `json:"priority"`
	Color                   string `json:"color"`
	Rules                   []Rule `json:"rules"`
	ScrollbackOnClearScreen bool   `json:"scrollback_on_clear_screen"`
	StatusSource            string `json:"status_source"`
	// Empty mappings use the standard OSC 9;4 interpretation.
	OSC            []string `json:"osc"`
	Interrupt      string   `json:"interrupt"`
	Exit           string   `json:"exit"`
	LongRunSeconds int      `json:"long_run_seconds"`
}
type Override struct {
	ID                      Kind      `json:"id"`
	Name                    *string   `json:"name,omitempty"`
	Enabled                 *bool     `json:"enabled,omitempty"`
	Priority                *int      `json:"priority,omitempty"`
	Color                   *string   `json:"color,omitempty"`
	Rules                   *[]Rule   `json:"rules,omitempty"`
	ScrollbackOnClearScreen *bool     `json:"scrollback_on_clear_screen,omitempty"`
	StatusSource            *string   `json:"status_source,omitempty"`
	OSC                     *[]string `json:"osc,omitempty"`
	Interrupt               *string   `json:"interrupt,omitempty"`
	Exit                    *string   `json:"exit,omitempty"`
	LongRunSeconds          *int      `json:"long_run_seconds,omitempty"`
}
type Config struct {
	Overrides []Override `json:"overrides,omitempty"`
	Custom    []Profile  `json:"custom,omitempty"`
}
type Process struct {
	PID     int      `json:"pid"`
	PGID    int      `json:"pgid"`
	Started string   `json:"started"`
	Name    string   `json:"name"`
	Args    []string `json:"args"`
}
type Match struct {
	ID       Kind   `json:"id"`
	Name     string `json:"name"`
	Priority int    `json:"priority"`
	Rule     int    `json:"rule"`
}
type Diagnostic struct {
	Process  Process  `json:"process"`
	Matches  []Match  `json:"matches"`
	Selected *Profile `json:"selected"`
}
type Registry struct{ profiles []Profile }

func DefaultOSC() []string { return []string{"Idle", "Running", "Error", "Running", "Paused"} }
func Builtins() []Profile {
	var out []Profile
	for _, d := range definitions {
		p := Profile{ID: d.kind, Name: d.label, Enabled: true, ScrollbackOnClearScreen: d.scrollbackOnClearScreen, OSC: DefaultOSC(), StatusSource: "osc", Interrupt: "ctrl-c", Exit: "sigterm"}
		for _, name := range d.names {
			p.Rules = append(p.Rules, Rule{All: []Condition{{"name", "exact", name}}})
		}
		for _, path := range d.pathMarkers {
			p.Rules = append(p.Rules, Rule{All: []Condition{{"path", "contains", path}}})
		}
		out = append(out, p)
	}
	return out
}
func IsBuiltin(id Kind) bool {
	for _, d := range definitions {
		if d.kind == id {
			return true
		}
	}
	return false
}

// isRetiredBuiltin recognizes IDs written by earlier releases. Their overrides
// remain dormant in Config so loading and saving settings preserves user edits
// without restoring removed builtin profiles to recognition or Agent Settings.
func isRetiredBuiltin(id Kind) bool {
	switch id {
	case GeminiCLI, Aider, CursorAgent, Amp, Crush, Goose, QwenCode, Droid, Grok:
		return true
	default:
		return false
	}
}

func Clone(c Config) Config {
	data, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(data, &out)
	return out
}
func Profiles(c Config) []Profile {
	out := Builtins()
	for i := range out {
		for _, o := range c.Overrides {
			if o.ID != out[i].ID {
				continue
			}
			p := &out[i]
			if o.Name != nil {
				p.Name = *o.Name
			}
			if o.Enabled != nil {
				p.Enabled = *o.Enabled
			}
			if o.Priority != nil {
				p.Priority = *o.Priority
			}
			if o.Color != nil {
				p.Color = *o.Color
			}
			if o.Rules != nil {
				p.Rules = *o.Rules
			}
			if o.StatusSource != nil {
				p.StatusSource = *o.StatusSource
			}
			if o.OSC != nil {
				p.OSC = *o.OSC
			}
			if o.Interrupt != nil {
				p.Interrupt = *o.Interrupt
			}
			if o.Exit != nil {
				p.Exit = *o.Exit
			}
			if o.ScrollbackOnClearScreen != nil {
				p.ScrollbackOnClearScreen = *o.ScrollbackOnClearScreen
			}
			if o.LongRunSeconds != nil {
				p.LongRunSeconds = *o.LongRunSeconds
			}
		}
	}
	out = append(out, c.Custom...)
	for i := range out {
		if out[i].StatusSource == "" {
			out[i].StatusSource = "auto"
		}
		if len(out[i].OSC) == 0 {
			out[i].OSC = DefaultOSC()
		}
	}
	return out
}

// Put persists only differences for builtins, so untouched fields inherit updates.
func (c *Config) Put(p Profile) {
	c.Remove(p.ID)
	if !IsBuiltin(p.ID) {
		c.Custom = append(c.Custom, p)
		return
	}
	var base Profile
	for _, b := range Builtins() {
		if b.ID == p.ID {
			base = b
		}
	}
	o := Override{ID: p.ID}
	if p.StatusSource != base.StatusSource {
		o.StatusSource = &p.StatusSource
	}
	if p.Name != base.Name {
		o.Name = &p.Name
	}
	if p.Enabled != base.Enabled {
		o.Enabled = &p.Enabled
	}
	if p.Priority != base.Priority {
		o.Priority = &p.Priority
	}
	if p.Color != base.Color {
		o.Color = &p.Color
	}
	if p.ScrollbackOnClearScreen != base.ScrollbackOnClearScreen {
		o.ScrollbackOnClearScreen = &p.ScrollbackOnClearScreen
	}
	if p.Interrupt != base.Interrupt {
		o.Interrupt = &p.Interrupt
	}
	if p.Exit != base.Exit {
		o.Exit = &p.Exit
	}
	if p.LongRunSeconds != base.LongRunSeconds {
		o.LongRunSeconds = &p.LongRunSeconds
	}
	a, _ := json.Marshal(p.Rules)
	b, _ := json.Marshal(base.Rules)
	if string(a) != string(b) {
		o.Rules = &p.Rules
	}
	a, _ = json.Marshal(p.OSC)
	b, _ = json.Marshal(base.OSC)
	if string(a) != string(b) {
		o.OSC = &p.OSC
	}
	if !reflect.DeepEqual(o, Override{ID: p.ID}) {
		c.Overrides = append(c.Overrides, o)
	}
}
func (c *Config) Remove(id Kind) {
	var os []Override
	for _, o := range c.Overrides {
		if o.ID != id {
			os = append(os, o)
		}
	}
	c.Overrides = os
	var ps []Profile
	for _, p := range c.Custom {
		if p.ID != id {
			ps = append(ps, p)
		}
	}
	c.Custom = ps
}
func Compile(c Config) (*Registry, error) {
	encoded, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	if len(encoded) > 256*1024 {
		return nil, fmt.Errorf("Agent configuration must be at most 256 KiB")
	}
	if len(c.Custom)+len(c.Overrides) > 128 {
		return nil, fmt.Errorf("at most 128 Agent definitions")
	}
	seen := map[Kind]bool{}
	for _, o := range c.Overrides {
		if (!IsBuiltin(o.ID) && !isRetiredBuiltin(o.ID)) || seen[o.ID] {
			return nil, fmt.Errorf("invalid or duplicate builtin override %s", o.ID)
		}
		seen[o.ID] = true
	}
	for _, p := range c.Custom {
		id, err := uuid.Parse(string(p.ID))
		if err != nil || id.Version() != 4 || id.Variant() != uuid.RFC4122 || id.String() != string(p.ID) || seen[p.ID] || IsBuiltin(p.ID) {
			return nil, fmt.Errorf("custom Agent ID must be a unique UUIDv4")
		}
		seen[p.ID] = true
	}
	ps := Profiles(Clone(c))
	for _, p := range ps {
		if err := Validate(p); err != nil {
			return nil, fmt.Errorf("%s: %w", p.Name, err)
		}
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Priority != ps[j].Priority {
			return ps[i].Priority > ps[j].Priority
		}
		return ps[i].ID < ps[j].ID
	})
	return &Registry{ps}, nil
}
func Validate(p Profile) error {
	if strings.IndexFunc(p.Name, unicode.IsControl) >= 0 {
		return fmt.Errorf("name must not contain control characters")
	}
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 128 {
		return fmt.Errorf("name must contain 1–128 bytes")
	}
	if p.Priority < -10000 || p.Priority > 10000 || p.LongRunSeconds < 0 || p.LongRunSeconds > 86400 {
		return fmt.Errorf("priority or long-run threshold out of range")
	}
	if p.Color != "" {
		if len(p.Color) != 7 || p.Color[0] != '#' {
			return fmt.Errorf("color must be #RRGGBB")
		}
		if _, err := strconv.ParseUint(p.Color[1:], 16, 24); err != nil {
			return fmt.Errorf("invalid color")
		}
	}
	if p.Interrupt != "ctrl-c" && p.Interrupt != "sigint" && p.Interrupt != "none" {
		return fmt.Errorf("interrupt must be ctrl-c, sigint or none")
	}
	if p.Exit != "sigterm" && p.Exit != "sighup" && p.Exit != "ctrl-d" && p.Exit != "none" {
		return fmt.Errorf("exit must be sigterm, sighup, ctrl-d or none")
	}
	if p.StatusSource != "" && p.StatusSource != "auto" && p.StatusSource != "osc" && p.StatusSource != "process" {
		return fmt.Errorf("status source must be auto, osc or process")
	}
	if len(p.OSC) != 0 && len(p.OSC) != 5 {
		return fmt.Errorf("OSC mapping must have five states")
	}
	for _, v := range p.OSC {
		switch v {
		case "Idle", "Running", "Error", "Paused", "Completed", "Unknown":
		default:
			return fmt.Errorf("invalid OSC state %q", v)
		}
	}
	if len(p.Rules) > 64 {
		return fmt.Errorf("at most 64 recognition rules")
	}
	for _, r := range p.Rules {
		if len(r.All) == 0 || len(r.All) > 8 {
			return fmt.Errorf("each rule needs 1–8 conditions")
		}
		for _, c := range r.All {
			switch c.Field {
			case "name", "executable", "path", "argument":
			default:
				return fmt.Errorf("unknown condition field %q", c.Field)
			}
			if c.Value == "" || len(c.Value) > 512 {
				return fmt.Errorf("condition needs 1–512 bytes")
			}
			switch c.Op {
			case "exact", "contains":
			case "glob":
				if _, err := filepath.Match(c.Value, ""); err != nil {
					return fmt.Errorf("invalid glob: %w", err)
				}
			default:
				return fmt.Errorf("unknown operator %q", c.Op)
			}
		}
	}
	return nil
}
func (r *Registry) Diagnose(p Process) Diagnostic {
	d := Diagnostic{Process: p, Matches: []Match{}}
	for _, profile := range r.profiles {
		if !profile.Enabled {
			continue
		}
		for i, rule := range profile.Rules {
			matched := true
			for _, c := range rule.All {
				if !conditionMatches(c, p) {
					matched = false
					break
				}
			}
			if matched {
				d.Matches = append(d.Matches, Match{profile.ID, profile.Name, profile.Priority, i})
				if d.Selected == nil {
					copy := profile
					d.Selected = &copy
				}
				break
			}
		}
	}
	return d
}
func (r *Registry) Detect(p Process) *DetectedAgent {
	d := r.Diagnose(p)
	if d.Selected == nil {
		return nil
	}
	runtimeProfile := *d.Selected
	runtimeProfile.Rules = nil // Recognition rules belong to settings/diagnostics, not every terminal snapshot.
	return &DetectedAgent{Kind: runtimeProfile.ID, Profile: &runtimeProfile, Run: &p}
}
func conditionMatches(c Condition, p Process) bool {
	var values []string
	switch c.Field {
	case "executable":
		values = []string{filepath.Base(p.Name)}
	case "argument":
		values = p.Args
	case "path":
		for _, a := range p.Args {
			if strings.Contains(a, "/") {
				values = append(values, a)
			}
		}
	case "name":
		values = []string{normalizeToken(p.Name)}
		if a := shellExecArgv0(p.Args); a != "" {
			values = append(values, normalizeToken(a))
		}
		for i, a := range p.Args {
			if i == 0 || strings.Contains(a, "/") {
				values = append(values, normalizeToken(a))
			}
		}
	}
	for _, value := range values {
		pattern := c.Value
		if c.Field == "name" {
			value, pattern = strings.ToLower(value), strings.ToLower(pattern)
		}
		switch c.Op {
		case "exact":
			if value == pattern {
				return true
			}
		case "contains":
			if strings.Contains(value, pattern) {
				return true
			}
		case "glob":
			if ok, _ := filepath.Match(pattern, value); ok {
				return true
			}
		}
	}
	return false
}

// ParseConfig rejects misspelled fields and non-object payloads rather than
// accidentally treating an unrelated JSON file as a request to reset profiles.
func ParseConfig(data []byte) (Config, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return Config{}, fmt.Errorf("Agent configuration must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return cfg, fmt.Errorf("Agent configuration must contain a single JSON object")
	}
	if _, err := Compile(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
