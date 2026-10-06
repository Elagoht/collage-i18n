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

A data handler or an action handler translates with `i18n.T(rc, key, pairs...)` —
anywhere collage hands you a `RenderContext`, in the request's locale:

```go
title := i18n.T(rc, "post.title", "name", post.Title)
```

In an action, this is how a flash message or a validation message is translated
before the handler answers — `.Required().Message(i18n.T(rc, "signup.email.required"))`.
Requires collage v0.39.0; before it, `T` returned the key in an action.

### Outside a render

An email, a background job, a message a goroutine sends: there is no render to
take a locale from. Keep the plugin's value and ask it for a `Translator`:

```go
tr := i18n.New(i18n.Options{FS: locales})
// Config.Plugins: []collage.Plugin{tr}

t := tr.In(user.Locale)
subject := t.T("mail.assigned", "card", card.Title)
reminder := t.TN("mail.due", days)

mail := template.Must(template.New("mail").Funcs(t.Funcs()).ParseFS(mails, "mail/*.html"))
```

| Method | |
| --- | --- |
| `In(locale)` | A Translator for the locale; one the application does not support, or `""`, is the default one |
| `Locale()` | The locale it translates in |
| `T`, `TN`, `TH` | `{{t}}`, `{{tn}}` and `{{th}}`, with the same catalogs, fallback and plurals |
| `Funcs()` | `t`, `tn` and `th` for a template of your own, such as an email's |

`T`, `TN` and `TH` return the text and nothing else, as `i18n.T` does; the
functions from `Funcs` fail the template on arguments that do not come in pairs,
as `{{t}}` fails a page. A key a Translator cannot find has no page to be reported
over, so it is logged, once per key and locale.

The catalogs are read when the application starts (`Handler`, `ListenAndServe`,
`Start`), not in `collage.New`. A Translator can be made at any time; one used
before the start translates every key to itself, and logs that it did.

## What is missing

A key the page's locale lacks falls back to the default locale's text, then to the
key itself. Either way it is reported:

- **in development**, over the page, as `missing-translation`;
- **in a static build**, under the page, and every key one catalog has and another
  lacks is listed as `untranslated`, in either direction: a key only the Turkish
  catalog has leaves the English pages showing the key itself;
- **at startup**, the untranslated keys are logged.

A plural form a locale's rule never picks is not missing from it: with a
`Plural` that gives Arabic `few`, the English catalog needs no `cart.few`. `zero`
is the catalog's choice rather than the language's, so one catalog having it and
another not is reported.

**`Strict`** turns the difference into a failure: the application does not start
while one catalog has a key another lacks, and the error lists them. Set it where
a missing translation should stop a deploy rather than reach a reader.

**One key, two spellings.** `{"nav.home": …}` and `{"nav": {"home": …}}` are the
same key. A catalog that writes both does not start: keeping either would leave
which text is shown to the order a map is walked in, and so to the restart.

In development the catalogs are read again on every request, so an edited catalog
shows on the next reload.

## Configuration

`FS` and `Plural` are Go; the directory and `Strict` can come from configuration:

```json
{ "elagoht/i18n": { "dir": "locales", "strict": true } }
```

## Changes

### v0.2.2

- Requires collage v0.49.0. Tests only: the test site gives its fragments
  typed data with `collage.Load` and `collage.DataHandler`, since
  `WithDataHandler` is gone. The plugin itself is unchanged.

### v0.2.0

- `Plugin.In(locale)` returns a `Translator`: `T`, `TN`, `TH` and `Funcs` outside a
  render, for an email or a job. What it cannot find is logged once per key.
- A key written twice in one catalog, dotted and nested, refuses to start. Which
  text it showed used to depend on the order a map was walked in.
- Keys missing from the default catalog are reported too, not only keys missing
  from the others; a plural form a locale's rule never picks is not reported.
- `Options.Strict` refuses to start while the catalogs differ.
