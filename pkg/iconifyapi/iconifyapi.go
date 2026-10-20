package iconifyapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/klauspost/compress/zstd"
	_ "github.com/mattn/go-sqlite3"
)

const (
	DefaultDatabasePath = "/usr/share/iconifyd/icons.sqlite"
	cacheSeconds        = 7 * 24 * 60 * 60
)

var (
	errIconNotFound = errors.New("icon not found")
	errInvalidIcon  = errors.New("invalid icon name")
	validName       = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

type GetSVGRequest struct {
	Collection string `json:"collection"`
	Name       string `json:"name"`
	Style      string `json:"style"`
}

type GetSVGResponse struct {
	SVG   string `json:"svg"`
	Found bool   `json:"found"`
}

type icon struct {
	Body   string  `json:"body"`
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Rotate int     `json:"rotate"`
	HFlip  bool    `json:"hFlip"`
	VFlip  bool    `json:"vFlip"`
}

type iconSet struct {
	Prefix   string          `json:"prefix"`
	Icons    map[string]icon `json:"icons"`
	NotFound []string        `json:"not_found,omitempty"`
}

type searchResponse struct {
	Icons       []string                   `json:"icons"`
	Total       int                        `json:"total"`
	Available   int                        `json:"available"`
	Limit       int                        `json:"limit"`
	Start       int                        `json:"start"`
	Collections map[string]json.RawMessage `json:"collections"`
	Request     map[string]string          `json:"request"`
}

type store struct {
	db         *sql.DB
	decoder    *zstd.Decoder
	icon       *sql.Stmt
	alias      *sql.Stmt
	collection *sql.Stmt
	info       *sql.Stmt
}

type Handler struct{ store *store }

func New(path string) (*Handler, error) {
	icons, err := openStore(path)
	if err != nil {
		return nil, err
	}
	return &Handler{store: icons}, nil
}

func openStore(path string) (*store, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: "immutable=1&mode=ro"}
	db, err := sql.Open("sqlite3", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	fail := func(err error) (*store, error) {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA query_only = ON; PRAGMA cache_size = -1024"); err != nil {
		return fail(fmt.Errorf("configure database: %w", err))
	}
	var dictionary []byte
	if err := db.QueryRow("SELECT data FROM dictionary WHERE id = 1").Scan(&dictionary); err != nil {
		return fail(fmt.Errorf("read zstd dictionary: %w", err))
	}
	decoder, err := zstd.NewReader(nil,
		zstd.WithDecoderDicts(dictionary),
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxMemory(8<<20),
	)
	if err != nil {
		return fail(fmt.Errorf("load zstd dictionary: %w", err))
	}
	prepare := func(query string) (*sql.Stmt, error) { return db.Prepare(query) }
	result := &store{db: db, decoder: decoder}
	result.icon, err = prepare(`
		SELECT left, top, width, height, rotate, h_flip, v_flip, body
		FROM icons WHERE collection = ? AND name = ?`)
	if err == nil {
		result.alias, err = prepare(`
			SELECT a.left, a.top, a.width, a.height, a.rotate, a.h_flip, a.v_flip, i.body
			FROM aliases AS a JOIN icons AS i ON i.id = a.icon_id
			WHERE a.collection = ? AND a.name = ?`)
	}
	if err == nil {
		result.collection, err = prepare(`
			SELECT EXISTS (SELECT 1 FROM collections WHERE prefix = ?)`)
	}
	if err == nil {
		result.info, err = prepare(`SELECT info FROM collections WHERE prefix = ?`)
	}
	if err != nil {
		result.Close()
		return nil, fmt.Errorf("prepare database queries: %w", err)
	}
	return result, nil
}

func (h *Handler) Close() error { return h.store.Close() }

func (h *Handler) GetSVG(request GetSVGRequest, response *GetSVGResponse) error {
	svg, found, err := h.getSVG(
		context.Background(),
		request.Collection,
		request.Name,
		request.Style,
		false,
	)
	if err != nil {
		return err
	}
	response.SVG, response.Found = svg, found
	return nil
}

func (s *store) Close() error {
	if s.icon != nil {
		s.icon.Close()
	}
	if s.alias != nil {
		s.alias.Close()
	}
	if s.collection != nil {
		s.collection.Close()
	}
	if s.info != nil {
		s.info.Close()
	}
	s.decoder.Close()
	return s.db.Close()
}

func (s *store) lookup(ctx context.Context, prefix, name string) (icon, error) {
	var result icon
	var compressed []byte
	var hFlip, vFlip int
	scan := func(row *sql.Row) error {
		return row.Scan(
			&result.Left, &result.Top, &result.Width, &result.Height,
			&result.Rotate, &hFlip, &vFlip, &compressed,
		)
	}
	err := scan(s.icon.QueryRowContext(ctx, prefix, name))
	if errors.Is(err, sql.ErrNoRows) {
		err = scan(s.alias.QueryRowContext(ctx, prefix, name))
	}
	if errors.Is(err, sql.ErrNoRows) {
		return icon{}, errIconNotFound
	}
	if err != nil {
		return icon{}, err
	}
	result.HFlip, result.VFlip = hFlip != 0, vFlip != 0
	body, err := s.decoder.DecodeAll(compressed, nil)
	if err != nil {
		return icon{}, fmt.Errorf("decompress %s:%s: %w", prefix, name, err)
	}
	result.Body = string(body)
	return result, nil
}

func (s *store) hasCollection(ctx context.Context, prefix string) (bool, error) {
	var found bool
	err := s.collection.QueryRowContext(ctx, prefix).Scan(&found)
	return found, err
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Access-Control-Allow-Origin", "*")
	response.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	response.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	response.Header().Set("Access-Control-Max-Age", "86400")
	response.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	if request.Method == http.MethodOptions {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", "GET, OPTIONS")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(request.URL.Path, "/")
	if path == "version" {
		writeJSON(response, http.StatusOK, map[string]string{"version": "1.0.0"}, false)
		return
	}
	if path == "search" {
		h.search(response, request)
		return
	}
	if path == "collections" {
		h.collections(response, request)
		return
	}
	if strings.HasSuffix(path, ".json") && !strings.Contains(strings.TrimSuffix(path, ".json"), "/") {
		h.iconData(response, request, strings.TrimSuffix(path, ".json"))
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) == 3 && parts[0] == "symbol" && strings.HasSuffix(parts[2], ".svg") {
		h.svg(response, request, parts[1], strings.TrimSuffix(parts[2], ".svg"), true)
		return
	}
	if len(parts) == 2 && strings.HasSuffix(parts[1], ".svg") {
		h.svg(response, request, parts[0], strings.TrimSuffix(parts[1], ".svg"), false)
		return
	}
	http.NotFound(response, request)
}

func (h *Handler) collections(response http.ResponseWriter, request *http.Request) {
	prefixes := splitNames(request.URL.Query().Get("prefixes"))
	statement := "SELECT prefix, info FROM collections"
	arguments := make([]any, len(prefixes))
	if len(prefixes) != 0 {
		placeholders := make([]string, len(prefixes))
		for index, prefix := range prefixes {
			if !validName.MatchString(prefix) {
				http.Error(response, "invalid prefixes", http.StatusBadRequest)
				return
			}
			placeholders[index], arguments[index] = "?", prefix
		}
		statement += " WHERE prefix IN (" + strings.Join(placeholders, ",") + ")"
	}
	rows, err := h.store.db.QueryContext(request.Context(), statement, arguments...)
	if err != nil {
		http.Error(response, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	collections := make(map[string]json.RawMessage)
	for rows.Next() {
		var prefix string
		var info []byte
		if err := rows.Scan(&prefix, &info); err != nil {
			http.Error(response, "database error", http.StatusInternalServerError)
			return
		}
		collections[prefix] = info
	}
	if err := rows.Err(); err != nil {
		http.Error(response, "database error", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(cacheSeconds))
	writeJSON(response, http.StatusOK, collections, request.URL.Query().Has("pretty"))
}

func (h *Handler) search(response http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	limit, err := queryInteger(query.Get("limit"), 64)
	if err != nil {
		http.Error(response, "invalid limit", http.StatusBadRequest)
		return
	}
	limit = max(32, min(limit, 999))
	start, err := queryInteger(query.Get("start"), 0)
	if err != nil || start < 0 {
		http.Error(response, "invalid start", http.StatusBadRequest)
		return
	}

	var where []string
	var arguments []any
	tokens := strings.FieldsFunc(strings.ToLower(query.Get("query")), func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsNumber(character)
	})
	for _, token := range tokens {
		where = append(where, `name LIKE ? COLLATE NOCASE`)
		arguments = append(arguments, "%"+token+"%")
	}
	if prefix := query.Get("prefix"); prefix != "" {
		if !validName.MatchString(prefix) {
			http.Error(response, "invalid prefix", http.StatusBadRequest)
			return
		}
		where = append(where, "collection = ?")
		arguments = append(arguments, prefix)
	}
	if prefixes := query.Get("prefixes"); prefixes != "" {
		var clauses []string
		for _, prefix := range strings.Split(prefixes, ",") {
			partial := strings.HasSuffix(prefix, "-")
			name := strings.TrimSuffix(prefix, "-")
			if !validName.MatchString(name) {
				http.Error(response, "invalid prefixes", http.StatusBadRequest)
				return
			}
			if partial {
				clauses = append(clauses, "collection LIKE ?")
				arguments = append(arguments, prefix+"%")
			} else {
				clauses = append(clauses, "collection = ?")
				arguments = append(arguments, prefix)
			}
		}
		where = append(where, "("+strings.Join(clauses, " OR ")+")")
	}
	if category := query.Get("category"); category != "" {
		where = append(where, "collection IN (SELECT prefix FROM collections WHERE category = ? COLLATE NOCASE)")
		arguments = append(arguments, category)
	}

	// shortcut: scan existing name indexes to avoid a larger FTS database; add FTS if measured latency requires it.
	from := ` FROM (
		SELECT collection, name FROM icons
		UNION ALL
		SELECT collection, name FROM aliases
	)`
	if len(where) != 0 {
		from += " WHERE " + strings.Join(where, " AND ")
	}
	var available int
	if err := h.store.db.QueryRowContext(request.Context(), "SELECT COUNT(*)"+from, arguments...).Scan(&available); err != nil {
		http.Error(response, "database error", http.StatusInternalServerError)
		return
	}
	statement := "SELECT collection, name" + from
	if len(tokens) == 0 {
		statement += " ORDER BY collection, name"
	} else {
		phrase := strings.Join(tokens, "-")
		statement += " ORDER BY CASE WHEN name = ? THEN 0 WHEN name LIKE ? THEN 1 ELSE 2 END, length(name), name, collection"
		arguments = append(arguments, phrase, phrase+"%")
	}
	statement += " LIMIT ? OFFSET ?"
	arguments = append(arguments, limit, start)

	rows, err := h.store.db.QueryContext(request.Context(), statement, arguments...)
	if err != nil {
		http.Error(response, "database error", http.StatusInternalServerError)
		return
	}
	icons := make([]string, 0)
	prefixes := make(map[string]bool)
	for rows.Next() {
		var prefix, name string
		if err := rows.Scan(&prefix, &name); err != nil {
			rows.Close()
			http.Error(response, "database error", http.StatusInternalServerError)
			return
		}
		icons = append(icons, prefix+":"+name)
		prefixes[prefix] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		http.Error(response, "database error", http.StatusInternalServerError)
		return
	}
	if err := rows.Close(); err != nil {
		http.Error(response, "database error", http.StatusInternalServerError)
		return
	}
	collections := make(map[string]json.RawMessage, len(prefixes))
	for prefix := range prefixes {
		var info []byte
		if err := h.store.info.QueryRowContext(request.Context(), prefix).Scan(&info); err != nil {
			http.Error(response, "database error", http.StatusInternalServerError)
			return
		}
		collections[prefix] = info
	}
	requestParameters := make(map[string]string, len(query))
	for name := range query {
		requestParameters[name] = query.Get(name)
	}
	result := searchResponse{
		Icons: icons, Total: len(icons), Available: available, Limit: limit, Start: start,
		Collections: collections, Request: requestParameters,
	}
	response.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(cacheSeconds))
	writeJSON(response, http.StatusOK, result, query.Has("pretty"))
}

func queryInteger(value string, defaultValue int) (int, error) {
	if value == "" {
		return defaultValue, nil
	}
	return strconv.Atoi(value)
}

func (h *Handler) iconData(response http.ResponseWriter, request *http.Request, prefix string) {
	if !validName.MatchString(prefix) {
		http.Error(response, "invalid collection prefix", http.StatusBadRequest)
		return
	}
	names := splitNames(request.URL.Query().Get("icons"))
	if len(names) == 0 {
		http.Error(response, "icons parameter is required", http.StatusBadRequest)
		return
	}
	result := iconSet{Prefix: prefix, Icons: make(map[string]icon, len(names))}
	for _, name := range names {
		if !validName.MatchString(name) {
			http.Error(response, "invalid icon name", http.StatusBadRequest)
			return
		}
		item, err := h.store.lookup(request.Context(), prefix, name)
		switch {
		case err == nil:
			result.Icons[name] = item
		case errors.Is(err, errIconNotFound):
			result.NotFound = append(result.NotFound, name)
		default:
			http.Error(response, "database error", http.StatusInternalServerError)
			return
		}
	}
	if len(result.Icons) == 0 {
		found, err := h.store.hasCollection(request.Context(), prefix)
		if err != nil {
			http.Error(response, "database error", http.StatusInternalServerError)
			return
		}
		if !found {
			http.NotFound(response, request)
			return
		}
	}
	response.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(cacheSeconds))
	writeJSON(response, http.StatusOK, result, request.URL.Query().Has("pretty"))
}

func (h *Handler) svg(response http.ResponseWriter, request *http.Request, prefix, name string, symbol bool) {
	svg, found, err := h.getSVG(request.Context(), prefix, name, "", symbol)
	if errors.Is(err, errInvalidIcon) {
		http.Error(response, "invalid icon name", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(response, "database error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(response, request)
		return
	}
	if color := request.URL.Query().Get("color"); color != "" {
		svg = strings.ReplaceAll(svg, "currentColor", html.EscapeString(color))
	}
	response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	contentType := "image/svg+xml; charset=utf-8"
	if symbol {
		contentType = "image/svg+xml"
	}
	response.Header().Set("Content-Type", contentType)
	if request.URL.Query().Has("download") {
		response.Header().Set("Content-Disposition", `attachment; filename="`+prefix+`-`+name+`.svg"`)
	}
	if symbol {
		svg = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" + svg
	}
	_, _ = response.Write([]byte(svg))
}

func (h *Handler) getSVG(ctx context.Context, prefix, name, style string, symbol bool) (string, bool, error) {
	if !validName.MatchString(prefix) || !validName.MatchString(name) {
		return "", false, errInvalidIcon
	}
	item, err := h.store.lookup(ctx, prefix, name)
	if errors.Is(err, errIconNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	body, left, top, width, height := renderBody(item)
	if symbol {
		body = `<symbol id="` + name + `">` + body + `</symbol>`
	}
	viewBox := joinNumbers(left, top, width, height)
	displayWidth := math.Ceil(width/height*100) / 100
	style = strings.Trim(style, "; ")
	if style != "" {
		style = ` style="` + html.EscapeString(style) + `;"`
	}
	return fmt.Sprintf(`<svg%s xmlns="http://www.w3.org/2000/svg" width="%gem" height="1em" viewBox="%s">%s</svg>`, style, displayWidth, viewBox, body), true, nil
}

func splitNames(value string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, name := range strings.Split(value, ",") {
		if name != "" && !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	return result
}

func writeJSON(response http.ResponseWriter, status int, value any, pretty bool) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	encoder := json.NewEncoder(response)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	_ = encoder.Encode(value)
}

func renderBody(item icon) (string, float64, float64, float64, float64) {
	body := item.Body
	left, top, width, height := item.Left, item.Top, item.Width, item.Height
	var transforms []string
	rotation := item.Rotate
	if item.HFlip && item.VFlip {
		rotation += 2
	} else if item.HFlip {
		transforms = append(transforms,
			"translate("+joinNumbers(width+left, -top)+")",
			"scale(-1 1)",
		)
		left, top = 0, 0
	} else if item.VFlip {
		transforms = append(transforms,
			"translate("+joinNumbers(-left, height+top)+")",
			"scale(1 -1)",
		)
		left, top = 0, 0
	}
	rotation = ((rotation % 4) + 4) % 4
	switch rotation {
	case 1:
		value := height/2 + top
		transforms = append([]string{"rotate(90 " + joinNumbers(value, value) + ")"}, transforms...)
	case 2:
		transforms = append([]string{"rotate(180 " + joinNumbers(width/2+left, height/2+top) + ")"}, transforms...)
	case 3:
		value := width/2 + left
		transforms = append([]string{"rotate(-90 " + joinNumbers(value, value) + ")"}, transforms...)
	}
	if rotation%2 == 1 {
		left, top = top, left
		width, height = height, width
	}
	if len(transforms) != 0 {
		body = `<g transform="` + strings.Join(transforms, " ") + `">` + body + `</g>`
	}
	return body, left, top, width, height
}

func joinNumbers(values ...float64) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.FormatFloat(value, 'f', -1, 64)
	}
	return strings.Join(parts, " ")
}
