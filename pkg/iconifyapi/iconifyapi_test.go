package iconifyapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	_ "github.com/mattn/go-sqlite3"
)

func testHandler(t *testing.T) *Handler {
	t.Helper()
	history := bytes.Repeat([]byte("svg path fill currentColor width height "), 16)
	dictionary, err := zstd.BuildDict(zstd.BuildDictOptions{
		ID:       1,
		Contents: [][]byte{append(history, "one"...), append(history, "two"...)},
		History:  history,
		Offsets:  [3]int{1, 4, 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderDict(dictionary))
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()

	path := filepath.Join(t.TempDir(), "icons.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE dictionary (id INTEGER PRIMARY KEY, data BLOB NOT NULL);
		CREATE TABLE collections (
			prefix TEXT PRIMARY KEY, category TEXT NOT NULL, info BLOB NOT NULL
		) WITHOUT ROWID;
		CREATE TABLE icons (
			id INTEGER PRIMARY KEY, collection TEXT NOT NULL, name TEXT NOT NULL,
			left REAL NOT NULL, top REAL NOT NULL, width REAL NOT NULL, height REAL NOT NULL,
			rotate INTEGER NOT NULL, h_flip INTEGER NOT NULL, v_flip INTEGER NOT NULL,
			body BLOB NOT NULL, UNIQUE (collection, name)
		);
		CREATE TABLE aliases (
			collection TEXT NOT NULL, name TEXT NOT NULL, icon_id INTEGER NOT NULL,
			left REAL NOT NULL, top REAL NOT NULL, width REAL NOT NULL, height REAL NOT NULL,
			rotate INTEGER NOT NULL, h_flip INTEGER NOT NULL, v_flip INTEGER NOT NULL,
			PRIMARY KEY (collection, name), FOREIGN KEY (icon_id) REFERENCES icons (id)
		) WITHOUT ROWID;
		INSERT INTO dictionary VALUES (1, ?);
		INSERT INTO collections VALUES ('test', 'Testing', '{"name":"Test icons","total":2,"category":"Testing"}');
		INSERT INTO icons VALUES (1, 'test', 'square', 0, 0, 16, 16, 0, 0, 0, ?);
		INSERT INTO aliases VALUES ('test', 'turned', 1, 2, 3, 16, 24, 1, 1, 0);
	`, dictionary, encoder.EncodeAll([]byte(`<path fill="currentColor"/>`), nil)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { result.Close() })
	return result
}

func TestSearch(t *testing.T) {
	handler := testHandler(t)
	for _, test := range []struct {
		url   string
		icons []string
	}{
		{"/search?query=square&category=testing&limit=32", []string{"test:square"}},
		{"/search?query=turn&prefix=test", []string{"test:turned"}},
		{"/search", []string{"test:square", "test:turned"}},
		{"/search?query=", []string{"test:square", "test:turned"}},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.url, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", test.url, response.Code, response.Body.String())
		}
		var result searchResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if strings.Join(result.Icons, ",") != strings.Join(test.icons, ",") || result.Total != len(test.icons) || result.Available != len(test.icons) || result.Limit < 32 {
			t.Errorf("%s: unexpected response: %#v", test.url, result)
		}
		if _, ok := result.Collections["test"]; !ok {
			t.Errorf("%s: collection metadata missing: %#v", test.url, result.Collections)
		}
	}
}

func TestSearchFiltersAndPagination(t *testing.T) {
	handler := testHandler(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/search?prefixes=test&start=1&limit=32", nil))
	var result searchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || strings.Join(result.Icons, ",") != "test:turned" || result.Start != 1 || result.Available != 2 {
		t.Fatalf("unexpected paginated response: %#v", result)
	}

	for _, path := range []string{"/search?limit=nope", "/search?start=-1", "/search?prefix=bad_name"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", path, response.Code)
		}
	}
}

func TestCollections(t *testing.T) {
	handler := testHandler(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/collections?prefixes=test", nil))
	var result map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !bytes.Contains(result["test"], []byte(`"name":"Test icons"`)) {
		t.Fatalf("unexpected collections response: %d %#v", response.Code, result)
	}
}

func TestLookupChecksIconThenAlias(t *testing.T) {
	icons := testHandler(t).store
	item, err := icons.lookup(context.Background(), "test", "turned")
	if err != nil {
		t.Fatal(err)
	}
	if item.Body != `<path fill="currentColor"/>` || item.Left != 2 || item.Top != 3 || item.Width != 16 || item.Height != 24 || item.Rotate != 1 || !item.HFlip {
		t.Fatalf("unexpected alias: %#v", item)
	}
	if _, err := icons.lookup(context.Background(), "test", "missing"); !errors.Is(err, errIconNotFound) {
		t.Fatalf("missing icon error: %v", err)
	}
}

func TestIconData(t *testing.T) {
	handler := testHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/test.json?icons=square,turned,missing", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	var result iconSet
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Prefix != "test" || len(result.Icons) != 2 || len(result.NotFound) != 1 || result.NotFound[0] != "missing" {
		t.Fatalf("unexpected response: %#v", result)
	}
	alias := result.Icons["turned"]
	if alias.Body == "" || alias.Left != 2 || alias.Top != 3 || alias.Width != 16 || alias.Height != 24 || alias.Rotate != 1 || !alias.HFlip {
		t.Fatalf("alias metadata was not returned: %#v", alias)
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "*" || response.Header().Get("Cache-Control") != "public, max-age=604800" {
		t.Fatalf("unexpected headers: %#v", response.Header())
	}
}

func TestIconDataErrors(t *testing.T) {
	handler := testHandler(t)
	for _, test := range []struct {
		url  string
		code int
	}{
		{"/test.json", http.StatusBadRequest},
		{"/bad_name.json?icons=square", http.StatusBadRequest},
		{"/unknown.json?icons=square", http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.url, nil))
		if response.Code != test.code {
			t.Errorf("%s: got %d, want %d", test.url, response.Code, test.code)
		}
	}
}

func TestSVGResolvesAliasTransformAndColor(t *testing.T) {
	handler := testHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/test/turned.svg?color=%23abc&download", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, value := range []string{
		`width="1.5em" height="1em" viewBox="0 0 24 16"`,
		`transform="rotate(90 12 12) translate(18 -3) scale(-1 1)"`,
		`fill="#abc"`,
	} {
		if !strings.Contains(body, value) {
			t.Errorf("SVG does not contain %q: %s", value, body)
		}
	}
	if response.Header().Get("Content-Disposition") != `attachment; filename="test-turned.svg"` {
		t.Fatalf("unexpected content disposition: %q", response.Header().Get("Content-Disposition"))
	}
	if response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("unexpected cache control: %q", response.Header().Get("Cache-Control"))
	}
}

func TestSymbolSVG(t *testing.T) {
	handler := testHandler(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/symbol/test/square.svg", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	if !strings.HasPrefix(response.Body.String(), `<?xml version="1.0" encoding="UTF-8"?>`) ||
		!strings.Contains(response.Body.String(), `<symbol id="square">`) {
		t.Fatalf("unexpected symbol SVG: %s", response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("unexpected cache control: %q", response.Header().Get("Cache-Control"))
	}
	if response.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("unexpected content type: %q", response.Header().Get("Content-Type"))
	}
}

func TestGetSVGRPC(t *testing.T) {
	handler := testHandler(t)
	var response GetSVGResponse
	if err := handler.GetSVG(GetSVGRequest{
		Collection: "test",
		Name:       "turned",
		Style:      " color:red ",
	}, &response); err != nil {
		t.Fatal(err)
	}
	if !response.Found ||
		!strings.Contains(response.SVG, `<svg style="color:red;"`) {
		t.Fatalf("unexpected RPC response: %#v", response)
	}

	response = GetSVGResponse{}
	if err := handler.GetSVG(GetSVGRequest{Collection: "test", Name: "missing"}, &response); err != nil {
		t.Fatal(err)
	}
	if response.Found || response.SVG != "" {
		t.Fatalf("unexpected missing icon response: %#v", response)
	}

	if err := handler.GetSVG(GetSVGRequest{Collection: "bad_name", Name: "square"}, &response); !errors.Is(err, errInvalidIcon) {
		t.Fatalf("unexpected invalid icon error: %v", err)
	}
}
