# elagoht/i18n

A collage plugin for translations: a catalog per locale, `{{t}}` in templates in
the locale the page is rendered in, plurals, and missing translations reported
where collage reports findings.

```go
//go:embed locales
var locales embed.FS

app, err := collage.New(&collage.Config{
	Locale:  collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
	Plugins: []collage.Plugin{i18n.New(i18n.Options{FS: locales})},
})
```

Requires collage v0.22.0 or later. Register it in `Config.Plugins`: it adds
template functions, which only a plugin registered there can.

collage already routes every request to a locale — by the path's prefix, the
default one otherwise — and builds links in it with `pageURL`. What it leaves to
this plugin is the words.

## Catalogs

One JSON file per supported locale, `locales/<locale>.json`, of nested keys:

```json
{
  "nav": { "home": "Home", "about": "About" },
  "greeting": "Hello, {name}!",
  "cart": { "zero": "Your cart is empty", "one": "{count} item", "other": "{count} items" },
  "welcome": "<strong>{name}</strong> joined the team"
}
```

The application does not start while a supported locale has no catalog.

## In templates

```html
<a href="{{pageURL "home"}}">{{t "nav.home"}}</a>
<p>{{t "greeting" "name" .User.Name}}</p>
<p>{{tn "cart" .Count}}</p>
<p>{{th "welcome" "name" .User.Name}}</p>
```

- `t` translates a key; name and value pairs fill `{name}`. The result is text,
  escaped like anything else.
- `tn` picks a plural form of the key for a count — `zero` when the catalog has one
  and the count is 0, `one`, `other` — and fills `{count}`.
- `th` is `t` for a translation holding markup. The catalog's markup is trusted, as
  a template is; the values filling it are escaped.

Plural forms are chosen by `Options.Plural(locale, n)`. The default knows `one`
for 1 and `other` for the rest, which fits English and Turkish; give your own for a
language with more forms.

## In Go

A data handler translates with `i18n.T(rc, key, pairs...)`:

```go
title := i18n.T(rc, "post.title", "name", post.Title)
```

## What is missing

A key the page's locale lacks falls back to the default locale's text, then to the
key itself. Either way it is reported:

- **in development**, over the page, as `missing-translation`;
- **in a static build**, under the page, and every key the default catalog has and
  another locale's lacks is listed as `untranslated`;
- **at startup**, the untranslated keys are logged.

In development the catalogs are read again on every request, so an edited catalog
shows on the next reload.

## Configuration

`FS` and `Plural` are Go; the directory can come from configuration:

```json
{ "elagoht/i18n": { "dir": "locales" } }
```
