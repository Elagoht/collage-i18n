// A collage plugin for translations: a catalog per locale, {{t}} in templates in
// the render's own locale, plurals, and missing translations reported where
// collage reports findings.
//
// It requires collage the way any consumer does, and reaches nothing the framework
// does not offer every plugin.
module github.com/Elagoht/collage-i18n

go 1.26

require github.com/Elagoht/collage v0.39.0
