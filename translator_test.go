package i18n_test

import (
	"bytes"
	"html/template"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	i18n "github.com/Elagoht/collage-i18n"
	"github.com/Elagoht/collage/pkg/collage"
)

// logs is a slog handler's output, safe to read while the app writes to it.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// started is an application with the plugin configured by opts, started, and
// what it logged.
func started(t *testing.T, opts i18n.Options) (*i18n.Plugin, *logs, error) {
	t.Helper()
	out := &logs{}
	plugin := i18n.New(opts)
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>{{t "nav.home"}}</p>`)}}, Root: "t"},
		Locale:   collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
		Plugins:  []collage.Plugin{plugin},
		Logger:   slog.New(slog.NewTextHandler(out, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	frag := collage.NewFragment("p", "p.html").Build()
	if err := app.RegisterPage(collage.NewPage("home").WithContent(frag).WithPath("en", "/").Build()); err != nil {
		t.Fatal(err)
	}
	return plugin, out, app.Start()
}

func TestTranslatesOutsideARender(t *testing.T) {
	plugin, _, err := started(t, i18n.Options{FS: catalogs})
	if err != nil {
		t.Fatal(err)
	}
	tr := plugin.In("tr")
	for _, c := range []struct{ got, want string }{
		{tr.Locale(), "tr"},
		{tr.T("nav.home"), "Ana sayfa"},
		{tr.T("greeting", "name", "İpek Işıl"), "Merhaba, İpek Işıl!"},
		{tr.TN("cart", 3), "3 ürün"},
		{tr.TN("cart", 0), "0 ürün"},
		{string(tr.TH("rich", "name", "<Ağa>")), "<strong>&lt;Ağa&gt;</strong> katıldı"},
		{tr.T("only.english"), "English only"},
		{plugin.In("en").TN("cart", 0), "Your cart is empty"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

// A locale the application does not support — a stale preference, an empty one —
// translates in the default locale, and says so.
func TestAnUnsupportedLocaleIsTheDefault(t *testing.T) {
	plugin, _, err := started(t, i18n.Options{FS: catalogs})
	if err != nil {
		t.Fatal(err)
	}
	for _, locale := range []string{"fr", ""} {
		tr := plugin.In(locale)
		if tr.Locale() != "en" || tr.T("nav.home") != "Home" {
			t.Errorf("In(%q): locale %q, nav.home %q", locale, tr.Locale(), tr.T("nav.home"))
		}
	}
}

func TestFuncsForTheApplicationsOwnTemplates(t *testing.T) {
	plugin, _, err := started(t, i18n.Options{FS: catalogs})
	if err != nil {
		t.Fatal(err)
	}
	tmpl := template.Must(template.New("mail").Funcs(plugin.In("tr").Funcs()).Parse(
		`{{t "greeting" "name" .}} {{tn "cart" 1}} {{th "rich" "name" .}}`))
	var out strings.Builder
	if err := tmpl.Execute(&out, "Şule"); err != nil {
		t.Fatal(err)
	}
	if want := "Merhaba, Şule! 1 ürün <strong>Şule</strong> katıldı"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}

	bad := template.Must(template.New("bad").Funcs(plugin.In("tr").Funcs()).Parse(`{{t "greeting" "name"}}`))
	if err := bad.Execute(&strings.Builder{}, nil); err == nil {
		t.Error("an odd number of arguments did not fail the template")
	}
}

// Outside a render there is no page to report a missing key over, so it is
// logged — once, however often it is asked for.
func TestAMissingKeyOutsideARenderIsLoggedOnce(t *testing.T) {
	plugin, out, err := started(t, i18n.Options{FS: catalogs})
	if err != nil {
		t.Fatal(err)
	}
	tr := plugin.In("tr")
	for range 3 {
		if got := tr.T("mail.nowhere"); got != "mail.nowhere" {
			t.Errorf("a missing key translated to %q", got)
		}
	}
	tr.T("only.english")
	logged := out.String()
	if n := strings.Count(logged, "mail.nowhere"); n != 1 {
		t.Errorf("missing key logged %d times:\n%s", n, logged)
	}
	if !strings.Contains(logged, "only.english") {
		t.Errorf("a key shown in the default locale was not logged:\n%s", logged)
	}
}

func TestTranslatingBeforeTheAppStartedIsTheKey(t *testing.T) {
	out := &logs{}
	plugin := i18n.New(i18n.Options{FS: catalogs})
	if _, err := collage.New(&collage.Config{
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`x`)}}, Root: "t"},
		Plugins:  []collage.Plugin{plugin},
		Logger:   slog.New(slog.NewTextHandler(out, nil)),
	}); err != nil {
		t.Fatal(err)
	}
	if got := plugin.In("tr").T("nav.home"); got != "nav.home" {
		t.Errorf("before start: %q", got)
	}
	if !strings.Contains(out.String(), "before the application started") {
		t.Errorf("not logged:\n%s", out.String())
	}
}

// Two spellings of one key, nested and dotted, would leave which text is shown
// to the order a map is walked in.
func TestTwoSpellingsOfOneKeyRefuseToStart(t *testing.T) {
	_, _, err := started(t, i18n.Options{FS: fstest.MapFS{
		"locales/en.json": {Data: []byte(`{"nav.home": "Home", "nav": {"home": "Start"}}`)},
		"locales/tr.json": {Data: []byte(`{"nav": {"home": "Ana sayfa"}}`)},
	}})
	if err == nil || !strings.Contains(err.Error(), `"nav.home"`) || !strings.Contains(err.Error(), "en.json") {
		t.Errorf("err = %v, want one naming nav.home in en.json", err)
	}
}

var diverging = fstest.MapFS{
	"locales/en.json": {Data: []byte(`{"nav": {"home": "Home"}, "only": {"english": "English only"}}`)},
	"locales/tr.json": {Data: []byte(`{"nav": {"home": "Ana sayfa"}, "sadece": {"turkce": "Yalnızca Türkçe"}}`)},
}

// A key the default catalog lacks is reported as well as one another lacks: the
// default locale's pages would show the key itself.
func TestDivergenceIsReportedBothWays(t *testing.T) {
	_, out, err := started(t, i18n.Options{FS: diverging})
	if err != nil {
		t.Fatal(err)
	}
	logged := out.String()
	for _, want := range []string{"only.english", "sadece.turkce"} {
		if !strings.Contains(logged, want) {
			t.Errorf("startup log lacks %s:\n%s", want, logged)
		}
	}
}

func TestStrictRefusesDivergence(t *testing.T) {
	_, _, err := started(t, i18n.Options{FS: diverging, Strict: true})
	if err == nil {
		t.Fatal("diverging catalogs started under Strict")
	}
	for _, want := range []string{`"only.english"`, "tr", `"sadece.turkce"`, "en"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err lacks %s: %v", want, err)
		}
	}

	matching := fstest.MapFS{
		"locales/en.json": {Data: []byte(`{"nav": {"home": "Home"}, "cart": {"one": "{count} item", "other": "{count} items"}}`)},
		"locales/tr.json": {Data: []byte(`{"nav": {"home": "Ana sayfa"}, "cart": {"one": "{count} ürün", "other": "{count} ürün"}}`)},
	}
	if _, _, err := started(t, i18n.Options{FS: matching, Strict: true}); err != nil {
		t.Errorf("matching catalogs refused under Strict: %v", err)
	}
}

// A plural form a locale's own rule never picks is not one it lacks: Arabic's
// "few" is no gap in the English catalog.
func TestAPluralFormALocaleNeverUsesIsNoGap(t *testing.T) {
	plural := func(locale string, n int) string {
		switch {
		case n == 1:
			return "one"
		case locale == "tr" && n%100 >= 3 && n%100 <= 10: // stands in for a language with "few"
			return "few"
		}
		return "other"
	}
	fsys := fstest.MapFS{
		"locales/en.json": {Data: []byte(`{"cart": {"one": "{count} item", "other": "{count} items"}}`)},
		"locales/tr.json": {Data: []byte(`{"cart": {"one": "a", "few": "b", "other": "c"}}`)},
	}
	if _, _, err := started(t, i18n.Options{FS: fsys, Strict: true, Plural: plural}); err != nil {
		t.Errorf("a form en never uses was taken for a gap: %v", err)
	}
}
