// Package i18n is a collage plugin for translations.
//
//	//go:embed locales
//	var locales embed.FS
//
//	app, err := collage.New(&collage.Config{
//		Locale:  collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
//		Plugins: []collage.Plugin{i18n.New(i18n.Options{FS: locales})},
//	})
//
// Each supported locale has a catalog, locales/<locale>.json, of nested keys:
//
//	{ "nav": { "home": "Home" }, "greeting": "Hello, {name}!",
//	  "cart": { "one": "{count} item", "other": "{count} items" } }
//
// and a template translates in the locale the page is rendered in:
//
//	<a href="/">{{t "nav.home"}}</a>
//	<p>{{t "greeting" "name" .Name}}</p>
//	<p>{{tn "cart" .Count}}</p>
//
// collage already routes a request to a locale — the path's prefix, the default
// otherwise — so a translation is only a lookup in the catalog of rc.Locale. A key
// the locale's catalog lacks falls back to the default locale's, then to the key
// itself, and is reported where collage reports findings: over the page in
// development, in a static build's report.
package i18n

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Elagoht/collage/pkg/collage"
)

// Name is the plugin's name, and the key its configuration is found under.
const Name = "elagoht/i18n"

// Options configures the plugin.
type Options struct {
	// FS holds the catalogs. Required.
	FS fs.FS `json:"-"`
	// Dir is the directory of the catalogs within FS. Default "locales".
	Dir string `json:"dir"`
	// Plural chooses the plural form of a count for a locale: "zero", "one",
	// "two", "few", "many" or "other". The default knows "one" for 1 and
	// "other" for the rest, which is right for English and Turkish and wrong for
	// Arabic or Polish; give a function of your own for those.
	Plural func(locale string, n int) string `json:"-"`
}

// Plugin serves translations.
type Plugin struct {
	opts Options
	dev  bool
	log  *slog.Logger

	defaultLocale string
	locales       []string

	mu       sync.RWMutex
	catalogs map[string]map[string]string
}

// New returns a plugin with opts as its starting point, which the application's
// own configuration is then decoded over.
func New(opts Options) *Plugin { return &Plugin{opts: opts} }

func (p *Plugin) Name() string                   { return Name }
func (p *Plugin) Version() string                { return "0.1.1" }
func (p *Plugin) Shutdown(context.Context) error { return nil }

// Configure adds {{t}}, {{tn}} and {{th}}.
func (p *Plugin) Configure(_ context.Context, host collage.ConfigHost) error {
	if err := host.Config(&p.opts); err != nil {
		return err
	}
	p.dev = host.DevMode()
	p.log = host.Logger()
	if p.opts.FS == nil {
		return fmt.Errorf("i18n: Options.FS is required: the catalogs are read from it")
	}
	if p.opts.Dir == "" {
		p.opts.Dir = "locales"
	}
	for name, fn := range map[string]func(rc *collage.RenderContext) any{ // any: html/template.FuncMap's own value type
		"t": func(rc *collage.RenderContext) any { // any: as above
			return func(key string, args ...any) (string, error) { return p.translate(rc, key, args) } // any: a template passes what it has
		},
		"th": func(rc *collage.RenderContext) any { // any: as above
			return func(key string, args ...any) (template.HTML, error) { return p.translateHTML(rc, key, args) } // any: as above
		},
		"tn": func(rc *collage.RenderContext) any { // any: as above
			return func(key string, n int, args ...any) (string, error) { return p.plural(rc, key, n, args) } // any: as above
		},
	} {
		if err := host.AddRenderFunc(name, fn); err != nil {
			return err
		}
	}
	return nil
}

// Init reads the catalogs, and refuses to start without one per locale.
func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	if p.opts.FS == nil {
		return fmt.Errorf("i18n: register the plugin in Config.Plugins, where Configure runs")
	}
	p.defaultLocale, p.locales = host.Locales()
	catalogs, err := p.load()
	if err != nil {
		return err
	}
	p.catalogs = catalogs
	for _, missing := range p.untranslated() {
		p.log.Warn("i18n: untranslated", "locale", missing.locale, "key", missing.key)
	}
	return nil
}

// load reads every locale's catalog.
func (p *Plugin) load() (map[string]map[string]string, error) {
	catalogs := make(map[string]map[string]string, len(p.locales))
	for _, locale := range p.locales {
		file := path.Join(p.opts.Dir, locale+".json")
		body, err := fs.ReadFile(p.opts.FS, file)
		if err != nil {
			return nil, fmt.Errorf("i18n: catalog for %q: %w", locale, err)
		}
		var tree map[string]any // any: a JSON object's values are strings or objects
		if err := json.Unmarshal(body, &tree); err != nil {
			return nil, fmt.Errorf("i18n: %s: %w", file, err)
		}
		flat := make(map[string]string)
		if err := flatten("", tree, flat); err != nil {
			return nil, fmt.Errorf("i18n: %s: %w", file, err)
		}
		catalogs[locale] = flat
	}
	return catalogs, nil
}

func flatten(prefix string, tree map[string]any, out map[string]string) error { // any: as in load
	for k, v := range tree {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch v := v.(type) {
		case string:
			out[key] = v
		case map[string]any: // any: as in load
			if err := flatten(key, v, out); err != nil {
				return err
			}
		default:
			return fmt.Errorf("key %q is neither a string nor an object", key)
		}
	}
	return nil
}

type gap struct{ locale, key string }

// untranslated lists the keys the default catalog has and another lacks.
func (p *Plugin) untranslated() []gap {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var gaps []gap
	for _, locale := range p.locales {
		if locale == p.defaultLocale {
			continue
		}
		for key := range p.catalogs[p.defaultLocale] {
			if _, ok := p.catalogs[locale][key]; !ok {
				gaps = append(gaps, gap{locale, key})
			}
		}
	}
	sort.Slice(gaps, func(i, j int) bool {
		if gaps[i].locale != gaps[j].locale {
			return gaps[i].locale < gaps[j].locale
		}
		return gaps[i].key < gaps[j].key
	})
	return gaps
}

// missingKey is where a render records the keys it could not find, for
// OnAfterRender to report.
const missingKey = Name + ":missing"

type missingSet struct {
	mu   sync.Mutex
	keys map[string]bool
}

// OnBeforeRender reloads the catalogs in development, so an edited catalog shows
// on the next request, and gives the render somewhere to record what it missed.
func (p *Plugin) OnBeforeRender(_ context.Context, ev *collage.BeforeRenderEvent) error {
	if p.dev {
		if catalogs, err := p.load(); err == nil {
			p.mu.Lock()
			p.catalogs = catalogs
			p.mu.Unlock()
		} else {
			p.log.Error("i18n: reload", "err", err)
		}
	}
	if ev.Context != nil {
		ev.Context.Set(missingKey, &missingSet{keys: make(map[string]bool)})
		ev.Context.Set(pluginKey, p)
	}
	return nil
}

// OnAfterRender reports the keys the page asked for and no catalog had.
func (p *Plugin) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	set, ok := ev.Data[missingKey].(*missingSet)
	if !ok {
		return nil
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	keys := make([]string, 0, len(set.keys))
	for k := range set.keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ev.Warn("missing-translation", k)
	}
	return nil
}

// OnBuildFinished reports every key the default locale's catalog has and another
// locale's lacks, which a page falls back to the default language for.
func (p *Plugin) OnBuildFinished(_ context.Context, ev *collage.BuildFinishedEvent) error {
	for _, g := range p.untranslated() {
		ev.Warn("", "untranslated", fmt.Sprintf("%q has no %s translation; the %s text is shown", g.key, g.locale, p.defaultLocale))
	}
	return nil
}

// T translates key in rc's locale, for a data handler: T(rc, "greeting", "name",
// user.Name). Its arguments are name and value pairs filling {name} in the text.
func T(rc *collage.RenderContext, key string, args ...any) string { // any: a value is printed as fmt prints it
	p, ok := collage.Get[*Plugin](rc, pluginKey)
	if !ok {
		return key
	}
	s, _ := p.translate(rc, key, args)
	return s
}

// pluginKey is where OnBeforeRender leaves the plugin for T.
const pluginKey = Name + ":plugin"

func (p *Plugin) lookup(rc *collage.RenderContext, key string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	locale := p.defaultLocale
	if rc != nil && rc.Locale != "" {
		locale = rc.Locale
	}
	if s, ok := p.catalogs[locale][key]; ok {
		return s, true
	}
	if s, ok := p.catalogs[p.defaultLocale][key]; ok {
		p.record(rc, key+" ("+locale+", shown in "+p.defaultLocale+")")
		return s, true
	}
	p.record(rc, key)
	return key, false
}

func (p *Plugin) record(rc *collage.RenderContext, key string) {
	if rc == nil {
		return
	}
	if set, ok := collage.Get[*missingSet](rc, missingKey); ok {
		set.mu.Lock()
		set.keys[key] = true
		set.mu.Unlock()
	}
}

func (p *Plugin) translate(rc *collage.RenderContext, key string, args []any) (string, error) { // any: as in T
	s, _ := p.lookup(rc, key)
	return fill(s, args, func(v string) string { return v })
}

func (p *Plugin) translateHTML(rc *collage.RenderContext, key string, args []any) (template.HTML, error) { // any: as in T
	s, _ := p.lookup(rc, key)
	// The catalog's markup is trusted, as a template is; what fills it is not.
	filled, err := fill(s, args, html.EscapeString)
	return template.HTML(filled), err // the catalog's own markup, with its arguments escaped
}

func (p *Plugin) plural(rc *collage.RenderContext, key string, n int, args []any) (string, error) { // any: as in T
	locale := p.defaultLocale
	if rc != nil && rc.Locale != "" {
		locale = rc.Locale
	}
	form := p.form(locale, n)
	p.mu.RLock()
	_, has := p.catalogs[locale][key+"."+form]
	_, hasDefault := p.catalogs[p.defaultLocale][key+"."+form]
	p.mu.RUnlock()
	if !has && !hasDefault {
		form = "other"
	}
	if n == 0 {
		p.mu.RLock()
		_, zero := p.catalogs[locale][key+".zero"]
		p.mu.RUnlock()
		if zero {
			form = "zero"
		}
	}
	s, _ := p.lookup(rc, key+"."+form)
	return fill(s, append([]any{"count", n}, args...), func(v string) string { return v }) // any: as in T
}

func (p *Plugin) form(locale string, n int) string {
	if p.opts.Plural != nil {
		return p.opts.Plural(locale, n)
	}
	if n == 1 {
		return "one"
	}
	return "other"
}

// fill replaces {name} in s with the value paired with name in args.
func fill(s string, args []any, escape func(string) string) (string, error) { // any: as in T
	if len(args)%2 != 0 {
		return s, fmt.Errorf("i18n: arguments come in name and value pairs, got %d", len(args))
	}
	if len(args) == 0 {
		return s, nil
	}
	pairs := make([]string, 0, len(args))
	for i := 0; i < len(args); i += 2 {
		name, ok := args[i].(string)
		if !ok {
			return s, fmt.Errorf("i18n: argument name %v is not a string", args[i])
		}
		pairs = append(pairs, "{"+name+"}", escape(printed(args[i+1])))
	}
	return strings.NewReplacer(pairs...).Replace(s), nil
}

func printed(v any) string { // any: as in T
	switch v := v.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}
