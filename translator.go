package i18n

import (
	"html/template"
	"log/slog"
	"slices"
)

// Translator translates in one locale outside a render: an email, a job, a
// message a goroutine sends. It follows the rules {{t}} does — the same
// catalogs, fallback and plurals — but a key it cannot find has no page to be
// reported over, so it is logged, once per key.
//
//	t := plugin.In(user.Locale)
//	subject := t.T("mail.assigned", "card", card.Title)
//
// The catalogs are read when the application starts. A Translator can be made at
// any time; one used before the start translates every key to itself.
type Translator struct {
	p      *Plugin
	locale string
}

// In returns a Translator for locale. A locale the application does not support,
// or none, is the default one.
func (p *Plugin) In(locale string) Translator { return Translator{p: p, locale: locale} }

// Locale is the locale t translates in: the one it was made for, or the default.
func (t Translator) Locale() string {
	t.p.mu.RLock()
	defer t.p.mu.RUnlock()
	if slices.Contains(t.p.locales, t.locale) {
		return t.locale
	}
	return t.p.defaultLocale
}

// T translates key, as {{t}} does; its arguments are name and value pairs
// filling {name}.
func (t Translator) T(key string, args ...any) string { // any: a value is printed as fmt prints it
	s, _ := t.p.translate(t.Locale(), key, args, t.miss())
	return s
}

// TN translates the plural form of key for n, as {{tn}} does, filling {count}.
func (t Translator) TN(key string, n int, args ...any) string { // any: as in T
	s, _ := t.p.plural(t.Locale(), key, n, args, t.miss())
	return s
}

// TH translates a key holding markup, as {{th}} does: the catalog's markup is
// kept and what fills it is escaped.
func (t Translator) TH(key string, args ...any) template.HTML { // any: as in T
	s, _ := t.p.translateHTML(t.Locale(), key, args, t.miss())
	return s
}

// Funcs is {{t}}, {{tn}} and {{th}} in t's locale, for a template of the
// application's own, such as an email's. Unlike T, they fail the template on
// arguments that do not come in pairs.
func (t Translator) Funcs() template.FuncMap {
	return template.FuncMap{
		"t": func(key string, args ...any) (string, error) { // any: a template passes what it has
			return t.p.translate(t.Locale(), key, args, t.miss())
		},
		"tn": func(key string, n int, args ...any) (string, error) { // any: as above
			return t.p.plural(t.Locale(), key, n, args, t.miss())
		},
		"th": func(key string, args ...any) (template.HTML, error) { // any: as above
			return t.p.translateHTML(t.Locale(), key, args, t.miss())
		},
	}
}

// miss logs what t could not find, once per key and locale.
func (t Translator) miss() func(string) {
	return func(key string) {
		p := t.p
		p.mu.RLock()
		early := p.catalogs == nil
		p.mu.RUnlock()
		if early {
			p.earlyLogged.Do(func() {
				p.logger().Warn("i18n: a translation was asked for before the application started; the key is shown", "key", key)
			})
			return
		}
		locale := t.Locale()
		if _, seen := p.logged.LoadOrStore(locale+"\x00"+key, true); !seen {
			p.logger().Warn("i18n: missing translation", "locale", locale, "key", key)
		}
	}
}

// logger is the application's logger, or slog's own before Configure has run.
func (p *Plugin) logger() *slog.Logger {
	if p.log != nil {
		return p.log
	}
	return slog.Default()
}
