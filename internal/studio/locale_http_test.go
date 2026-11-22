package studio

import (
	"net/http/httptest"
	"testing"

	"aigongzuotai/internal/localeip"
)

func TestLocaleCatalogDescriptionProvenance(t *testing.T) {
	base := Config{Mode: "demo"}
	builtin, err := loadModelCatalog(base)
	if err != nil {
		t.Fatal(err)
	}
	base.ModelCatalogFile = writeCatalogFixture(t, defaultCatalogConfig(base))
	custom, err := loadModelCatalog(base)
	if err != nil {
		t.Fatal(err)
	}
	if builtin.Public.Version == custom.Public.Version {
		t.Fatal("changed public display metadata must invalidate cached responses")
	}
	for i, model := range builtin.Public.Models {
		if !model.BuiltinDescription || custom.Public.Models[i].BuiltinDescription ||
			model.Version != custom.Public.Models[i].Version || builtin.Entries[model.ID].Model.BuiltinDescription {
			t.Fatal("display provenance must distinguish catalog origins without changing execution snapshots")
		}
	}
}

func TestLocaleHTTPBeforeAuthentication(t *testing.T) {
	resolver, err := localeip.New("127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	// No session, DB or application context is needed to choose a login-page language.
	app := &App{locales: resolver, config: Config{AuthMode: "required"}}
	handler := app.Handler()
	r := httptest.NewRequest("GET", "http://localhost/api/locale", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "168.95.1.1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, r)
	if response.Code != 200 || response.Body.String() != "{\"locale\":\"zh-TW\"}\n" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected public response %d %s %v", response.Code, response.Body.String(), response.Header())
	}
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, r)
	if response.Code != 403 {
		t.Fatalf("cross-site request returned %d", response.Code)
	}
}
