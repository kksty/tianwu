package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A refresh must not revalidate the whole bundle: hashed assets are immutable,
// the shell is not.
func TestStaticCacheHeaders(t *testing.T) {
	static := Static()
	entries, err := files.ReadDir("static/assets")
	if err != nil {
		t.Fatal(err)
	}
	assets := []string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			assets = append(assets, "/static/assets/"+entry.Name())
			if len(assets) == 2 {
				break
			}
		}
	}
	if len(assets) < 2 {
		t.Fatal("embedded assets missing")
	}
	for _, tc := range []struct {
		path string
		want string
	}{
		{assets[0], assetCache},
		{assets[1], assetCache},
		{"/static/index.html", "no-cache"},
	} {
		rec := httptest.NewRecorder()
		static.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if got := rec.Header().Get("Cache-Control"); got != tc.want {
			t.Fatalf("%s: Cache-Control = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestMissingStaticAssetDoesNotGetImmutableCaching(t *testing.T) {
	rec := httptest.NewRecorder()
	Static().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/assets/missing-fixture.js", nil))
	if rec.Code != 404 || rec.Header().Get("Cache-Control") == assetCache {
		t.Fatalf("missing asset cached: %d %s", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

func TestIndexRevalidates(t *testing.T) {
	rec := httptest.NewRecorder()
	Index()(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index Cache-Control = %q", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d", rec.Code)
	}
}
