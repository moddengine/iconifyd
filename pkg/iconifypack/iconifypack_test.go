package iconifypack

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func pointer[T any](value T) *T {
	return &value
}

func TestResolveAlias(t *testing.T) {
	collection := collectionJSON{
		Prefix: "test",
		optionalProps: optionalProps{
			Width: pointer(20.0), Height: pointer(10.0),
		},
		Icons: map[string]iconJSON{
			"arrow": {
				Body: `<path/>`,
				optionalProps: optionalProps{
					Left: pointer(2.0), Top: pointer(3.0),
				},
			},
		},
		Aliases: map[string]aliasJSON{
			"arrow-up": {
				Parent: "flipped",
				optionalProps: optionalProps{
					Rotate: pointer(1),
				},
			},
			"flipped": {Parent: "arrow", optionalProps: optionalProps{HFlip: pointer(true)}},
		},
	}

	resolved, err := resolveCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	got := resolved["arrow-up"]
	if got.Canonical != "arrow" || got.Left != 2 || got.Top != 3 || got.Width != 20 || got.Height != 10 {
		t.Fatalf("unexpected viewBox: %g %g %g %g", got.Left, got.Top, got.Width, got.Height)
	}
	if got.Body != `<path/>` || got.Rotate != 1 || !got.HFlip || got.VFlip {
		t.Fatalf("unexpected resolved alias: %#v", got)
	}
}

func TestResolveCollectionRejectsBrokenAliases(t *testing.T) {
	collection := collectionJSON{
		Prefix: "test",
		Icons:  map[string]iconJSON{"ok": {Body: `<path/>`}},
		Aliases: map[string]aliasJSON{
			"a": {Parent: "b"},
			"b": {Parent: "a"},
		},
	}
	if _, err := resolveCollection(collection); err == nil {
		t.Fatal("expected cyclic alias error")
	}
}

func TestResolveCollectionAllowsBlankIcon(t *testing.T) {
	resolved, err := resolveCollection(collectionJSON{
		Prefix: "test",
		Icons:  map[string]iconJSON{"blank": {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved["blank"].Body != "" {
		t.Fatalf("unexpected blank icon body %q", resolved["blank"].Body)
	}
	if resolved["blank"].Canonical != "blank" {
		t.Fatalf("unexpected canonical icon %q", resolved["blank"].Canonical)
	}
}

func TestWriteDatabaseRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`<path fill="currentColor" d="M0 0h16v16H0z"/>`)
	bodyPath := filepath.Join(dir, "icon.xml")
	if err := os.WriteFile(bodyPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	history := bytes.Repeat([]byte("common svg path fill stroke width height "), 16)
	samples := [][]byte{
		append(append([]byte(nil), history...), "circle red"...),
		append(append([]byte(nil), history...), "square blue"...),
		append(append([]byte(nil), history...), "triangle green"...),
		append(append([]byte(nil), history...), "line black"...),
	}
	dict, err := zstd.BuildDict(zstd.BuildDictOptions{
		ID:       1,
		Contents: samples,
		History:  history,
		Offsets:  [3]int{1, 4, 8},
		Level:    zstd.SpeedDefault,
	})
	if err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(dir, "icons.sqlite")
	if err := writeDatabase(databasePath, dict, []collectionRecord{{
		Prefix: "test", Category: "Test", Info: []byte(`{"name":"Test icons","category":"Test"}`),
	}}, []iconRecord{{
		Collection: "test", Name: "square",
		Width: 16, Height: 16, BodyPath: bodyPath,
	}}, []aliasRecord{{
		Collection: "test", Name: "box", IconName: "square",
		Width: 16, Height: 16, Rotate: 1, HFlip: true,
	}}); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite3", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var storedDict, compressed []byte
	var width, height float64
	if err := db.QueryRow(`
		SELECT dictionary.data, icons.width, icons.height, icons.body
		FROM icons CROSS JOIN dictionary
		WHERE collection = 'test' AND name = 'square'
	`).Scan(&storedDict, &width, &height, &compressed); err != nil {
		t.Fatal(err)
	}
	if width != 16 || height != 16 || !bytes.Equal(storedDict, dict) {
		t.Fatal("stored metadata does not match")
	}
	var aliasBody []byte
	var iconName string
	var rotate, hFlip int
	if err := db.QueryRow(`
		SELECT icons.body, icons.name, aliases.rotate, aliases.h_flip
		FROM aliases
		JOIN icons ON icons.id = aliases.icon_id
		WHERE aliases.collection = 'test' AND aliases.name = 'box'
	`).Scan(&aliasBody, &iconName, &rotate, &hFlip); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(aliasBody, compressed) || iconName != "square" || rotate != 1 || hFlip != 1 {
		t.Fatal("alias did not resolve to its canonical icon")
	}
	var info string
	if err := db.QueryRow("SELECT info FROM collections WHERE prefix = 'test'").Scan(&info); err != nil || info != `{"name":"Test icons","category":"Test"}` {
		t.Fatalf("collection info is %q: %v", info, err)
	}
	var pageSize int
	if err := db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil || pageSize != 32768 {
		t.Fatalf("page size is %d: %v", pageSize, err)
	}
	var foreignKeyErrors int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_foreign_key_check").Scan(&foreignKeyErrors); err != nil || foreignKeyErrors != 0 {
		t.Fatalf("foreign key errors: %d: %v", foreignKeyErrors, err)
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderDicts(storedDict))
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	decoded, err := decoder.DecodeAll(compressed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, body) {
		t.Fatalf("decoded body %q, want %q", decoded, body)
	}
}

func TestFailedBuildPreservesExistingOutput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "icons.sqlite")
	want := []byte("existing")
	if err := os.WriteFile(output, want, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := build(filepath.Join(t.TempDir(), "missing"), output); err == nil {
		t.Fatal("expected build failure")
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output changed to %q", got)
	}
}

func TestExtractRejectsInvalidIconsWithoutDroppingValidCollection(t *testing.T) {
	input, xml := t.TempDir(), t.TempDir()
	data := []byte(`{
  "prefix":"test", "width":24, "height":24,
  "icons":{
   "valid":{"body":"<path/>"},
   "broken":{"body":"<path/>","width":0,"height":0}
  },
  "aliases":{
   "valid-alias":{"parent":"valid"},
   "broken-alias":{"parent":"valid","width":0},
   "broken-parent":{"parent":"broken","width":24,"height":24}
  }
 }`)
	if err := os.WriteFile(filepath.Join(input, "test.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	collections, icons, aliases, err := extract(input, xml)
	if err != nil {
		t.Fatal(err)
	}
	if len(collections) != 1 || len(icons) != 1 || icons[0].Name != "valid" || len(aliases) != 1 || aliases[0].Name != "valid-alias" {
		t.Fatalf("invalid entries were retained or valid ones lost: %v %v %v", collections, icons, aliases)
	}
}
