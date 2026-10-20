package iconifypack

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
	_ "github.com/mattn/go-sqlite3"
)

const dictionarySize = 8 * 1024

type optionalProps struct {
	Left   *float64 `json:"left"`
	Top    *float64 `json:"top"`
	Width  *float64 `json:"width"`
	Height *float64 `json:"height"`
	Rotate *int     `json:"rotate"`
	HFlip  *bool    `json:"hFlip"`
	VFlip  *bool    `json:"vFlip"`
}

type iconJSON struct {
	optionalProps
	Body string `json:"body"`
}

type aliasJSON struct {
	optionalProps
	Parent string `json:"parent"`
}

type collectionJSON struct {
	optionalProps
	Prefix  string               `json:"prefix"`
	Info    json.RawMessage      `json:"info"`
	Icons   map[string]iconJSON  `json:"icons"`
	Aliases map[string]aliasJSON `json:"aliases"`
}

type collectionRecord struct {
	Prefix   string
	Category string
	Info     json.RawMessage
}

type icon struct {
	Body                     string
	Canonical                string
	Left, Top, Width, Height float64
	Rotate                   int
	HFlip, VFlip             bool
}

type iconRecord struct {
	Collection               string
	Name                     string
	Left, Top, Width, Height float64
	Rotate                   int
	HFlip, VFlip             bool
	BodyPath                 string
}

type aliasRecord struct {
	Collection               string
	Name                     string
	IconName                 string
	Left, Top, Width, Height float64
	Rotate                   int
	HFlip, VFlip             bool
}

func Run(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: iconifyd pack <iconify-json-directory> <output.sqlite>")
	}
	return build(args[0], args[1])
}

func build(inputDir, outputPath string) error {
	workDir, err := os.MkdirTemp("", "iconifydb-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(workDir)

	xmlDir := filepath.Join(workDir, "xml")
	if err := os.Mkdir(xmlDir, 0o755); err != nil {
		return fmt.Errorf("create XML directory: %w", err)
	}
	collections, icons, aliases, err := extract(inputDir, xmlDir)
	if err != nil {
		return err
	}

	dictPath := filepath.Join(workDir, "dictionary")
	if err := trainDictionary(xmlDir, dictPath); err != nil {
		return err
	}
	dict, err := os.ReadFile(dictPath)
	if err != nil {
		return fmt.Errorf("read dictionary: %w", err)
	}

	outputPath, err = filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}
	tempFile, err := os.CreateTemp(filepath.Dir(outputPath), ".iconify-*.sqlite")
	if err != nil {
		return fmt.Errorf("create temporary database: %w", err)
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temporary database: %w", err)
	}
	if err := os.Remove(tempPath); err != nil {
		return fmt.Errorf("prepare temporary database: %w", err)
	}
	defer os.Remove(tempPath)

	if err := writeDatabase(tempPath, dict, collections, icons, aliases); err != nil {
		return err
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		return fmt.Errorf("replace output database: %w", err)
	}
	fmt.Printf("Stored %d icons and %d aliases with a %d-byte dictionary in %s\n", len(icons), len(aliases), len(dict), outputPath)
	return nil
}

func extract(inputDir, xmlDir string) ([]collectionRecord, []iconRecord, []aliasRecord, error) {
	entries, err := os.ReadDir(inputDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read Iconify directory: %w", err)
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			paths = append(paths, filepath.Join(inputDir, entry.Name()))
		}
	}
	if len(paths) == 0 {
		return nil, nil, nil, fmt.Errorf("no JSON collections found in %s", inputDir)
	}
	sort.Strings(paths)

	prefixes := make(map[string]string, len(paths))
	var collections []collectionRecord
	var icons []iconRecord
	var aliases []aliasRecord
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("read %s: %w", path, err)
		}
		var collection collectionJSON
		if err := json.Unmarshal(data, &collection); err != nil {
			return nil, nil, nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if collection.Prefix == "" || collection.Icons == nil {
			return nil, nil, nil, fmt.Errorf("%s is not an Iconify collection", path)
		}
		if previous, exists := prefixes[collection.Prefix]; exists {
			return nil, nil, nil, fmt.Errorf("duplicate collection prefix %q in %s and %s", collection.Prefix, previous, path)
		}
		for name := range collection.Aliases {
			if _, exists := collection.Icons[name]; exists {
				return nil, nil, nil, fmt.Errorf("%s contains both an icon and alias named %q", path, name)
			}
		}
		prefixes[collection.Prefix] = path
		if len(collection.Info) == 0 {
			collection.Info = json.RawMessage(`{}`)
		}
		var info struct {
			Category string `json:"category"`
		}
		if err := json.Unmarshal(collection.Info, &info); err != nil {
			return nil, nil, nil, fmt.Errorf("parse %s collection info: %w", path, err)
		}
		collections = append(collections, collectionRecord{
			Prefix: collection.Prefix, Category: info.Category, Info: collection.Info,
		})

		resolved, err := resolveCollection(collection)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("resolve %s: %w", path, err)
		}
		iconNames := make([]string, 0, len(collection.Icons))
		for name := range collection.Icons {
			iconNames = append(iconNames, name)
		}
		sort.Strings(iconNames)
		for _, name := range iconNames {
			item := resolved[name]
			if item.Width <= 0 || item.Height <= 0 {
				return nil, nil, nil, fmt.Errorf("%s/%s has invalid dimensions %gx%g", collection.Prefix, name, item.Width, item.Height)
			}
			bodyPath := filepath.Join(xmlDir, fmt.Sprintf("%09d.xml", len(icons)))
			if err := os.WriteFile(bodyPath, []byte(item.Body), 0o644); err != nil {
				return nil, nil, nil, fmt.Errorf("write %s/%s XML: %w", collection.Prefix, name, err)
			}
			icons = append(icons, iconRecord{
				Collection: collection.Prefix,
				Name:       name,
				Left:       item.Left,
				Top:        item.Top,
				Width:      item.Width,
				Height:     item.Height,
				Rotate:     item.Rotate,
				HFlip:      item.HFlip,
				VFlip:      item.VFlip,
				BodyPath:   bodyPath,
			})
		}
		aliasNames := make([]string, 0, len(collection.Aliases))
		for name := range collection.Aliases {
			aliasNames = append(aliasNames, name)
		}
		sort.Strings(aliasNames)
		for _, name := range aliasNames {
			item := resolved[name]
			if item.Width <= 0 || item.Height <= 0 {
				return nil, nil, nil, fmt.Errorf("%s/%s has invalid dimensions %gx%g", collection.Prefix, name, item.Width, item.Height)
			}
			aliases = append(aliases, aliasRecord{
				Collection: collection.Prefix,
				Name:       name,
				IconName:   item.Canonical,
				Left:       item.Left,
				Top:        item.Top,
				Width:      item.Width,
				Height:     item.Height,
				Rotate:     item.Rotate,
				HFlip:      item.HFlip,
				VFlip:      item.VFlip,
			})
		}
	}
	return collections, icons, aliases, nil
}

func resolveCollection(collection collectionJSON) (map[string]icon, error) {
	base := icon{Left: 0, Top: 0, Width: 16, Height: 16}
	base = merge(base, collection.optionalProps)
	resolved := make(map[string]icon, len(collection.Icons)+len(collection.Aliases))
	resolving := make(map[string]bool)

	var resolve func(string) (icon, error)
	resolve = func(name string) (icon, error) {
		if item, ok := resolved[name]; ok {
			return item, nil
		}
		if resolving[name] {
			return icon{}, fmt.Errorf("cyclic alias at %q", name)
		}
		resolving[name] = true
		defer delete(resolving, name)

		if source, ok := collection.Icons[name]; ok {
			item := merge(base, source.optionalProps)
			item.Body = source.Body
			item.Canonical = name
			resolved[name] = item
			return item, nil
		}
		alias, ok := collection.Aliases[name]
		if !ok {
			return icon{}, fmt.Errorf("missing icon or alias %q", name)
		}
		if alias.Parent == "" {
			return icon{}, fmt.Errorf("alias %q has no parent", name)
		}
		parent, err := resolve(alias.Parent)
		if err != nil {
			return icon{}, fmt.Errorf("alias %q: %w", name, err)
		}
		item := merge(parent, alias.optionalProps)
		resolved[name] = item
		return item, nil
	}

	for name := range collection.Icons {
		if _, err := resolve(name); err != nil {
			return nil, err
		}
	}
	for name := range collection.Aliases {
		if _, err := resolve(name); err != nil {
			return nil, err
		}
	}
	return resolved, nil
}

func merge(parent icon, child optionalProps) icon {
	result := parent
	if child.Left != nil {
		result.Left = *child.Left
	}
	if child.Top != nil {
		result.Top = *child.Top
	}
	if child.Width != nil {
		result.Width = *child.Width
	}
	if child.Height != nil {
		result.Height = *child.Height
	}
	if child.Rotate != nil {
		result.Rotate = (result.Rotate + *child.Rotate) % 4
	}
	if child.HFlip != nil && *child.HFlip {
		result.HFlip = !result.HFlip
	}
	if child.VFlip != nil && *child.VFlip {
		result.VFlip = !result.VFlip
	}
	return result
}

func trainDictionary(xmlDir, outputPath string) error {
	cmd := exec.Command(
		"zstd",
		"--train-fastcover",
		fmt.Sprintf("--maxdict=%d", dictionarySize),
		"--dictID=1",
		"-r",
		xmlDir,
		"-o",
		outputPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("train zstd dictionary: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func writeDatabase(path string, dict []byte, collections []collectionRecord, icons []iconRecord, aliases []aliasRecord) error {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return fmt.Errorf("open SQLite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`
		PRAGMA page_size = 32768;
		PRAGMA journal_mode = OFF;
		PRAGMA synchronous = OFF;
		PRAGMA foreign_keys = ON;
		CREATE TABLE dictionary (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			data BLOB NOT NULL
		);
		CREATE TABLE collections (
			prefix TEXT PRIMARY KEY,
			category TEXT NOT NULL,
			info BLOB NOT NULL
		) WITHOUT ROWID;
		CREATE TABLE icons (
			id INTEGER PRIMARY KEY,
			collection TEXT NOT NULL,
			name TEXT NOT NULL,
			left REAL NOT NULL,
			top REAL NOT NULL,
			width REAL NOT NULL,
			height REAL NOT NULL,
			rotate INTEGER NOT NULL,
			h_flip INTEGER NOT NULL,
			v_flip INTEGER NOT NULL,
			body BLOB NOT NULL,
			UNIQUE (collection, name)
		);
		CREATE TABLE aliases (
			collection TEXT NOT NULL,
			name TEXT NOT NULL,
			icon_id INTEGER NOT NULL,
			left REAL NOT NULL,
			top REAL NOT NULL,
			width REAL NOT NULL,
			height REAL NOT NULL,
			rotate INTEGER NOT NULL,
			h_flip INTEGER NOT NULL,
			v_flip INTEGER NOT NULL,
			PRIMARY KEY (collection, name),
			FOREIGN KEY (icon_id) REFERENCES icons (id)
		) WITHOUT ROWID;
	`); err != nil {
		return fmt.Errorf("create SQLite schema: %w", err)
	}

	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderDict(dict), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return fmt.Errorf("create zstd encoder: %w", err)
	}
	defer encoder.Close()
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin SQLite transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO dictionary (id, data) VALUES (1, ?)", dict); err != nil {
		return fmt.Errorf("insert dictionary: %w", err)
	}
	for _, collection := range collections {
		if _, err := tx.Exec(
			"INSERT INTO collections (prefix, category, info) VALUES (?, ?, ?)",
			collection.Prefix, collection.Category, []byte(collection.Info),
		); err != nil {
			return fmt.Errorf("insert collection %s: %w", collection.Prefix, err)
		}
	}
	statement, err := tx.Prepare(`
		INSERT INTO icons (collection, name, left, top, width, height, rotate, h_flip, v_flip, body)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare icon insert: %w", err)
	}
	defer statement.Close()
	iconIDs := make(map[string]int64, len(icons))
	for _, item := range icons {
		body, err := os.ReadFile(item.BodyPath)
		if err != nil {
			return fmt.Errorf("read %s/%s XML: %w", item.Collection, item.Name, err)
		}
		compressed := encoder.EncodeAll(body, nil)
		result, err := statement.Exec(
			item.Collection, item.Name,
			item.Left, item.Top, item.Width, item.Height,
			item.Rotate, item.HFlip, item.VFlip, compressed,
		)
		if err != nil {
			return fmt.Errorf("insert %s/%s: %w", item.Collection, item.Name, err)
		}
		iconID, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("get %s/%s ID: %w", item.Collection, item.Name, err)
		}
		iconIDs[item.Collection+"\x00"+item.Name] = iconID
	}
	if err := statement.Close(); err != nil {
		return fmt.Errorf("close icon insert: %w", err)
	}
	statement, err = tx.Prepare(`
		INSERT INTO aliases (collection, name, icon_id, left, top, width, height, rotate, h_flip, v_flip)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare alias insert: %w", err)
	}
	for _, item := range aliases {
		iconID, exists := iconIDs[item.Collection+"\x00"+item.IconName]
		if !exists {
			return fmt.Errorf("alias %s/%s references missing icon %s", item.Collection, item.Name, item.IconName)
		}
		if _, err := statement.Exec(
			item.Collection, item.Name, iconID,
			item.Left, item.Top, item.Width, item.Height,
			item.Rotate, item.HFlip, item.VFlip,
		); err != nil {
			return fmt.Errorf("insert alias %s/%s: %w", item.Collection, item.Name, err)
		}
	}
	if err := statement.Close(); err != nil {
		return fmt.Errorf("close alias insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit SQLite database: %w", err)
	}
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("check SQLite database: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("SQLite integrity check failed: %s", integrity)
	}
	return nil
}
