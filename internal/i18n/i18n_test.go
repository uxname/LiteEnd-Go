package i18n

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestTranslator(t *testing.T) *Translator {
	t.Helper()
	tr, err := New(slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return tr
}

func ctxWithLang(lang string) context.Context {
	return context.WithValue(context.Background(), langKey, lang)
}

func TestTranslate_English(t *testing.T) {
	t.Parallel()
	tr := newTestTranslator(t)
	got := tr.Translate(ctxWithLang("en"), "hello", map[string]string{"username": "Bob"})
	require.Equal(t, "Hello Bob!", got)
}

func TestTranslate_Russian(t *testing.T) {
	t.Parallel()
	tr := newTestTranslator(t)
	got := tr.Translate(ctxWithLang("ru"), "hello", map[string]string{"username": "Bob"})
	require.Equal(t, "Привет Bob!", got)
}

func TestTranslate_FallsBackToEnglish(t *testing.T) {
	t.Parallel()
	tr := newTestTranslator(t)
	// French is not provided → fall back to the English default.
	got := tr.Translate(ctxWithLang("fr"), "hello", map[string]string{"username": "Bob"})
	require.Equal(t, "Hello Bob!", got)
}

func TestTranslate_MissingKeyReturnsKey(t *testing.T) {
	t.Parallel()
	tr := newTestTranslator(t)
	got := tr.Translate(ctxWithLang("en"), "does.not.exist", nil)
	require.Equal(t, "does.not.exist", got)
}

func TestMiddleware_StoresAcceptLanguage(t *testing.T) {
	t.Parallel()
	tr := newTestTranslator(t)

	var translated string
	h := tr.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		translated = tr.Translate(r.Context(), "hello", map[string]string{"username": "Bob"})
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Language", "ru")
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.Equal(t, "Привет Bob!", translated)
}
