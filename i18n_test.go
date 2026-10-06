package i18n_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	i18n "github.com/Elagoht/collage-i18n"
	"github.com/Elagoht/collage/pkg/collage"
)

var catalogs = fstest.MapFS{
	"locales/en.json": {Data: []byte(`{"nav": {"home": "Home"}, "greeting": "Hello, {name}!",
		"cart": {"zero": "Your cart is empty", "one": "{count} item", "other": "{count} items"},
		"rich": "<strong>{name}</strong> joined", "only": {"english": "English only"}}`)},
	"locales/tr.json": {Data: []byte(`{"nav": {"home": "Ana sayfa"}, "greeting": "Merhaba, {name}!",
		"cart": {"one": "{count} ürün", "other": "{count} ürün"}, "rich": "<strong>{name}</strong> katıldı"}`)},
}

const page = `<nav>{{t "nav.home"}}</nav><p>{{t "greeting" "name" .}}</p>` +
	`<p>{{tn "cart" 0}}|{{tn "cart" 1}}|{{tn "cart" 3}}</p><p>{{th "rich" "name" "<Ada>"}}</p>` +
	`<p>{{t "only.english"}}</p><p>{{t "nowhere"}}</p>`

func site(t *testing.T, dev bool, fsys fstest.MapFS) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		DevMode:  dev,
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(page)}}, Root: "t"},
		Locale:   collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
		Plugins:  []collage.Plugin{i18n.New(i18n.Options{FS: fsys})},
	})
	if err != nil {
		t.Fatal(err)
	}
	frag := collage.NewFragment("p", "p.html").WithData(collage.Load(func(_ context.Context, rc *collage.RenderContext) (string, error) {
		return i18n.T(rc, "nav.home") + " & Ada", nil
	})).Static().Build() // reads only the locale, so a build writes it
	if err := app.RegisterPage(collage.NewPage("home").WithContent(frag).WithPath("en", "/").WithPath("tr", "/").Build()); err != nil {
		t.Fatal(err)
	}
	return app
}

func get(app *collage.App, path string) string {
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Body.String()
}

func TestTranslatesInTheRendersLocale(t *testing.T) {
	app := site(t, false, catalogs)
	en := get(app, "/")
	for _, want := range []string{
		"<nav>Home</nav>", "<p>Hello, Home &amp; Ada!</p>",
		"<p>Your cart is empty|1 item|3 items</p>",
		"<p><strong>&lt;Ada&gt;</strong> joined</p>",
		"<p>English only</p>", "<p>nowhere</p>",
	} {
		if !strings.Contains(en, want) {
			t.Errorf("en lacks %s\n%s", want, en)
		}
	}
	tr := get(app, "/tr")
	for _, want := range []string{
		"<nav>Ana sayfa</nav>", "<p>Merhaba, Ana sayfa &amp; Ada!</p>",
		"<p>0 ürün|1 ürün|3 ürün</p>", // Turkish has no zero form: "other" it is
		"<p><strong>&lt;Ada&gt;</strong> katıldı</p>",
		"<p>English only</p>", // falls back to the default locale
	} {
		if !strings.Contains(tr, want) {
			t.Errorf("tr lacks %s\n%s", want, tr)
		}
	}
}

// What a page asked for and no catalog had is shown in development and listed in
// a build, with the keys another locale lacks.
func TestMissingTranslationsAreReported(t *testing.T) {
	dev := site(t, true, catalogs)
	body := get(dev, "/tr")
	for _, want := range []string{"missing-translation", "nowhere", "only.english (tr, shown in en)"} {
		if !strings.Contains(body, want) {
			t.Errorf("overlay lacks %q", want)
		}
	}

	builder, err := collage.NewBuilder(site(t, false, catalogs), collage.BuildOptions{OutDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	report, err := builder.Build(context.Background())
	if err != nil && !errors.Is(err, collage.ErrBuildFindings) {
		t.Fatal(err)
	}
	var rules []string
	for _, f := range report.Findings {
		rules = append(rules, f.Rule+":"+f.Path+":"+f.Message)
	}
	joined := strings.Join(rules, "\n")
	for _, want := range []string{"missing-translation:/:nowhere", `untranslated::"only.english" has no tr translation`, `untranslated::"cart.zero" has no tr translation`} {
		if !strings.Contains(joined, want) {
			t.Errorf("build findings lack %s:\n%s", want, joined)
		}
	}
}

func TestEveryLocaleNeedsACatalog(t *testing.T) {
	app := site(t, false, fstest.MapFS{"locales/en.json": {Data: []byte(`{}`)}})
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("started without a tr catalog: %d", rec.Code)
	}
}

// TestT_TranslatesInAnAction pins framework-issue 002: i18n.T must translate in an
// action handler, not return the key. An action runs before any render, so the
// plugin is not in the render's shared data; Init's middleware leaves it in the
// request context, where T now also looks.
func TestT_TranslatesInAnAction(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t"},
		Locale:   collage.LocaleConfig{Default: "tr", Supported: []string{"tr", "en"}},
		Security: collage.SecurityConfig{CSRFKey: []byte(strings.Repeat("k", 32))},
		Plugins:  []collage.Plugin{i18n.New(i18n.Options{FS: catalogs})},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var inAction string
	act := collage.NewAction("a").WithPath("tr", "/a").WithMethods(http.MethodPost).WithoutCSRF().
		WithHandler(func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			inAction = i18n.T(rc, "nav.home")
			return collage.NoContent(http.StatusNoContent), nil
		}).Build()
	if err := app.RegisterAction(act); err != nil {
		t.Fatalf("RegisterAction: %v", err)
	}

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/a", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if inAction != "Ana sayfa" {
		t.Errorf("i18n.T in an action = %q, want the tr translation %q, not the key", inAction, "Ana sayfa")
	}
}
