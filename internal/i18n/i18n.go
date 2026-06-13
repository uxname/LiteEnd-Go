// Package i18n provides message translation (go-i18n) with an Accept-Language
// middleware and a fallback to English.
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

//go:embed locales/*.json
var localesFS embed.FS

const defaultLang = "en"

type ctxKey struct{}

var langKey ctxKey

// Translator loads message bundles and resolves localized strings.
type Translator struct {
	bundle *i18n.Bundle
	log    *slog.Logger
}

// New builds a Translator from the embedded locale files.
func New(log *slog.Logger) (*Translator, error) {
	bundle := i18n.NewBundle(language.English)
	bundle.RegisterUnmarshalFunc("json", json.Unmarshal)

	for _, name := range []string{"locales/en.json", "locales/ru.json"} {
		if _, err := bundle.LoadMessageFileFS(localesFS, name); err != nil {
			return nil, err
		}
	}
	return &Translator{bundle: bundle, log: log}, nil
}

// Middleware stores the request language (from Accept-Language) in the context.
func (t *Translator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := r.Header.Get("Accept-Language")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), langKey, lang)))
	})
}

// Translate resolves a message id in the request language, falling back to
// English. args fill template placeholders ({{.name}}).
func (t *Translator) Translate(ctx context.Context, key string, args map[string]string) string {
	lang, _ := ctx.Value(langKey).(string)
	localizer := i18n.NewLocalizer(t.bundle, lang, defaultLang)

	data := make(map[string]any, len(args))
	for k, v := range args {
		data[k] = v
	}

	msg, err := localizer.Localize(&i18n.LocalizeConfig{
		MessageID:    key,
		TemplateData: data,
	})
	if err != nil {
		t.log.Warn("translation missing", "key", key, "error", err)
		return key
	}
	return msg
}
