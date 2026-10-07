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
//
// Outside a render — an email, a job — Plugin.In(locale) returns a Translator
// with the same rules, whose misses are logged.
package i18n

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
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
	// Strict refuses to start while the catalogs differ: a key one locale has and
	// another lacks. Without it the difference is logged at startup and listed in
	// a static build's report, and the page falls back to the default locale's
	// text, or to the key itself.
	Strict bool `json:"strict"`
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

	// What a Translator has logged, so each missing key is logged once.
	logged      sync.Map
	earlyLogged sync.Once
}

// New returns a plugin with opts as its starting point, which the application's
// own configuration is then decoded over.
func New(opts Options) *Plugin { return &Plugin{opts: opts} }

func (p *Plugin) Name() string                   { return Name }
func (p *Plugin) Version() string                { return "0.2.4" }
func (p *Plugin) Shutdown(context.Context) error { return nil }

// Configure adds {{t}}, {{tn}} and {{th}}.
func (p *Plugin) Configure(_ context.Context, host collage.ConfigHost) error {
	var err error
	if p.opts, err = collage.PluginConfig(host, p.opts); err != nil {
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
			return func(key string, args ...any) (string, error) {
				return p.translate(p.localeOf(rc), key, args, p.recorder(rc))
			} // any: a template passes what it has
		},
		"th": func(rc *collage.RenderContext) any { // any: as above
			return func(key string, args ...any) (template.HTML, error) { // any: as above
				return p.translateHTML(p.localeOf(rc), key, args, p.recorder(rc))
			}
		},
		"tn": func(rc *collage.RenderContext) any { // any: as above
			return func(key string, n int, args ...any) (string, error) { // any: as above
				return p.plural(p.localeOf(rc), key, n, args, p.recorder(rc))
			}
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
	defaultLocale, locales := host.Locales()
	p.mu.Lock()
	p.defaultLocale, p.locales = defaultLocale, locales
	p.mu.Unlock()
	catalogs, err := p.load()
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.catalogs = catalogs
	p.mu.Unlock()
	gaps := p.untranslated()
	if p.opts.Strict && len(gaps) > 0 {
		return strictError(gaps)
	}
	for _, missing := range gaps {
		p.log.Warn("i18n: untranslated", "locale", missing.locale, "key", missing.key)
	}
	// Leave the plugin in every request's context, so T reaches it in an action
	// too. An action runs before any render, so OnBeforeRender has not left the
	// plugin in the render's values yet; a flash message or a validation
	// message translated in the action handler would otherwise read back the key.
	return host.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxPluginKey{}, p)))
		})
	})
}

// ctxPluginKey is where Init's middleware leaves the plugin in the request
// context, for T to find in an action — the counterpart to pluginKey, which
// OnBeforeRender leaves in a render's values.
type ctxPluginKey struct{}

// strictError lists what the catalogs differ by, the first twenty of it.
func strictError(gaps []gap) error {
	const shown = 20
	lines := make([]string, 0, shown+1)
	for i, g := range gaps {
		if i == shown {
			lines = append(lines, fmt.Sprintf("and %d more", len(gaps)-shown))
			break
		}
		lines = append(lines, fmt.Sprintf("%q has no %s translation", g.key, g.locale))
	}
	return fmt.Errorf("i18n: the catalogs differ, and Options.Strict is set: %s", strings.Join(lines, "; "))
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
			// {"nav.home": …} and {"nav": {"home": …}} are one key. Keeping either
			// would leave which text is shown to the order a map is walked in.
			if _, taken := out[key]; taken {
				return fmt.Errorf("key %q is written twice, once with dots and once nested", key)
			}
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

// untranslated lists the keys one locale's catalog has and another's lacks, in
// either direction: a key the default catalog lacks leaves its pages showing the
// key itself.
func (p *Plugin) untranslated() []gap {
	p.mu.RLock()
	defer p.mu.RUnlock()
	all := make(map[string]bool)
	for _, catalog := range p.catalogs {
		for key := range catalog {
			all[key] = true
		}
	}
	var gaps []gap
	for _, locale := range p.locales {
		forms := p.formsOf(locale)
		for key := range all {
			if _, ok := p.catalogs[locale][key]; !ok && p.needs(locale, key, forms) {
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

// pluralForms are the forms a plural key can end in.
var pluralForms = map[string]bool{"zero": true, "one": true, "two": true, "few": true, "many": true, "other": true}

// formsOf is every plural form locale's rule picks, found by asking it.
func (p *Plugin) formsOf(locale string) map[string]bool {
	forms := make(map[string]bool)
	for n := range 1000 {
		forms[p.form(locale, n)] = true
	}
	return forms
}

// needs reports whether locale's catalog should have key. A plural form its
// rule never picks it does not: English has no "few" to translate. "zero" is
// the catalog's choice, not the language's, and is always needed.
func (p *Plugin) needs(locale, key string, forms map[string]bool) bool {
	i := strings.LastIndexByte(key, '.')
	if i < 0 {
		return true
	}
	form := key[i+1:]
	if !pluralForms[form] || form == "zero" || form == "other" || forms[form] {
		return true
	}
	for _, catalog := range p.catalogs {
		if _, plural := catalog[key[:i]+".other"]; plural {
			return false
		}
	}
	return true
}

// missingKey is where a render records the keys it could not find, for
// OnAfterRender to report.
var missingKey = collage.NewKey[*missingSet](Name + ":missing")

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
		missingKey.Set(ev.Context, &missingSet{keys: make(map[string]bool)})
		pluginKey.Set(ev.Context, p)
	}
	return nil
}

// OnAfterRender reports the keys the page asked for and no catalog had.
func (p *Plugin) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	set, ok := missingKey.In(ev.Values)
	if !ok || set == nil {
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
		ev.Warn("", "untranslated", fmt.Sprintf("%q has no %s translation; %s", g.key, g.locale, p.shownInstead(g)))
	}
	return nil
}

// shownInstead says what a page shows for a key its locale lacks.
func (p *Plugin) shownInstead(g gap) string {
	p.mu.RLock()
	_, fallback := p.catalogs[p.defaultLocale][g.key]
	p.mu.RUnlock()
	if g.locale != p.defaultLocale && fallback {
		return "the " + p.defaultLocale + " text is shown"
	}
	return "the key itself is shown"
}

// T translates key in rc's locale, for a data handler: T(rc, "greeting", "name",
// user.Name). Its arguments are name and value pairs filling {name} in the text.
func T(rc *collage.RenderContext, key string, args ...any) string { // any: a value is printed as fmt prints it
	p, ok := pluginKey.Get(rc)
	if !ok {
		// No render left the plugin in the render's values: an action, which runs
		// before any render. Init's middleware left it in the request context.
		p, ok = pluginFromRequest(rc)
	}
	if !ok {
		return key
	}
	s, _ := p.translate(p.localeOf(rc), key, args, p.recorder(rc))
	return s
}

// pluginFromRequest finds the plugin in rc's request context, where Init's
// middleware leaves it. It is how T reaches the plugin outside a render — in an
// action handler, which collage hands a RenderContext but runs before any render.
func pluginFromRequest(rc *collage.RenderContext) (*Plugin, bool) {
	if rc == nil || rc.Request == nil {
		return nil, false
	}
	p, ok := rc.Request.Context().Value(ctxPluginKey{}).(*Plugin)
	return p, ok
}

// pluginKey is where OnBeforeRender leaves the plugin for T.
var pluginKey = collage.NewKey[*Plugin](Name + ":plugin")

// localeOf is the locale rc renders in.
func (p *Plugin) localeOf(rc *collage.RenderContext) string {
	if rc != nil && rc.Locale != "" {
		return rc.Locale
	}
	return p.defaultLocale
}

// lookup finds key in locale's catalog, then the default locale's, and tells miss
// what it could not find there: the key, or the key and the locale it was shown in.
// miss is called with the lock released: a Translator's takes it again.
func (p *Plugin) lookup(locale, key string, miss func(string)) string {
	p.mu.RLock()
	s, ok := p.catalogs[locale][key]
	fallback, inDefault := p.catalogs[p.defaultLocale][key]
	p.mu.RUnlock()
	switch {
	case ok:
		return s
	case inDefault:
		miss(key + " (" + locale + ", shown in " + p.defaultLocale + ")")
		return fallback
	}
	miss(key)
	return key
}

// recorder is where a render's misses go: the page's findings.
func (p *Plugin) recorder(rc *collage.RenderContext) func(string) {
	return func(key string) {
		if rc == nil {
			return
		}
		if set, ok := missingKey.Get(rc); ok && set != nil {
			set.mu.Lock()
			set.keys[key] = true
			set.mu.Unlock()
		}
	}
}

func (p *Plugin) translate(locale, key string, args []any, miss func(string)) (string, error) { // any: as in T
	return fill(p.lookup(locale, key, miss), args, func(v string) string { return v })
}

func (p *Plugin) translateHTML(locale, key string, args []any, miss func(string)) (template.HTML, error) { // any: as in T
	// The catalog's markup is trusted, as a template is; what fills it is not.
	filled, err := fill(p.lookup(locale, key, miss), args, html.EscapeString)
	return template.HTML(filled), err // the catalog's own markup, with its arguments escaped
}

func (p *Plugin) plural(locale, key string, n int, args []any, miss func(string)) (string, error) { // any: as in T
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
	s := p.lookup(locale, key+"."+form, miss)
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
