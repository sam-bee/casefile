package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaFS embed.FS

type app struct {
	db     *pgxpool.Pool
	search *searchClient
	static string
}

func main() {
	ctx := context.Background()
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	elasticURL := env("ELASTICSEARCH_URL", "http://elasticsearch:9200")
	staticDir := env("STATIC_DIR", "/app/public")
	port := env("PORT", "8080")

	var db *pgxpool.Pool
	var err error
	for attempt := 0; attempt < 30; attempt++ {
		db, err = pgxpool.New(ctx, databaseURL)
		if err == nil {
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = db.Ping(pingCtx)
			cancel()
		}
		if err == nil {
			break
		}
		if db != nil {
			db.Close()
		}
		log.Printf("waiting for PostgreSQL: %v", err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Fatalf("PostgreSQL unavailable: %v", err)
	}
	defer db.Close()

	if err := installSchema(ctx, db); err != nil {
		log.Fatalf("install schema: %v", err)
	}

	application := &app{db: db, search: newSearchClient(elasticURL), static: staticDir}
	go application.indexLoop(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/api", application.api)
	mux.HandleFunc("/api/", application.api)
	mux.HandleFunc("/", application.assets)

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("casefile listening on :%s", port)
	log.Fatal(server.ListenAndServe())
}

func env(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func installSchema(ctx context.Context, db *pgxpool.Pool) error {
	raw, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := migrateLegacyNames(ctx, tx); err != nil {
		return err
	}
	// pgx uses PostgreSQL's simple protocol for SQL without arguments, which
	// accepts a full migration script and keeps semicolons inside string values.
	if _, err := tx.Exec(ctx, string(raw)); err != nil {
		return fmt.Errorf("schema statement: %w", err)
	}
	return tx.Commit(ctx)
}

func migrateLegacyNames(ctx context.Context, tx pgx.Tx) error {
	for _, pair := range [][2]string{
		{"items", "casenotes"},
		{"comments", "casenote_comments"},
		{"links", "casenote_links"},
	} {
		var oldExists, newExists bool
		if err := tx.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL, to_regclass($2) IS NOT NULL",
			"public."+pair[0], "public."+pair[1]).Scan(&oldExists, &newExists); err != nil {
			return err
		}
		if oldExists && newExists {
			return fmt.Errorf("both legacy %s and new %s tables exist", pair[0], pair[1])
		}
		if oldExists {
			if _, err := tx.Exec(ctx, "ALTER TABLE "+pair[0]+" RENAME TO "+pair[1]); err != nil {
				return fmt.Errorf("rename %s: %w", pair[0], err)
			}
		}
	}
	for _, change := range [][3]string{
		{"casenote_comments", "item_id", "casenote_id"},
		{"casenote_links", "from_item_id", "from_casenote_id"},
		{"casenote_links", "to_item_id", "to_casenote_id"},
	} {
		var oldExists, newExists bool
		query := "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND column_name=$2), " +
			"EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND column_name=$3)"
		if err := tx.QueryRow(ctx, query, change[0], change[1], change[2]).Scan(&oldExists, &newExists); err != nil {
			return err
		}
		if oldExists && newExists {
			return fmt.Errorf("both %s and %s columns exist in %s", change[1], change[2], change[0])
		}
		if oldExists {
			if _, err := tx.Exec(ctx, "ALTER TABLE "+change[0]+" RENAME COLUMN "+change[1]+" TO "+change[2]); err != nil {
				return fmt.Errorf("rename %s.%s: %w", change[0], change[1], err)
			}
		}
	}
	for _, pair := range [][2]string{
		{"items_tags_idx", "casenotes_tags_idx"},
		{"items_updated_idx", "casenotes_updated_idx"},
		{"items_project_updated_idx", "casenotes_project_updated_idx"},
		{"items_search_dirty_idx", "casenotes_search_dirty_idx"},
		{"comments_item_idx", "casenote_comments_idx"},
		{"links_from_idx", "casenote_links_from_idx"},
		{"links_to_idx", "casenote_links_to_idx"},
		{"links_project_idx", "casenote_links_project_idx"},
	} {
		var oldExists, newExists bool
		if err := tx.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL, to_regclass($2) IS NOT NULL",
			"public."+pair[0], "public."+pair[1]).Scan(&oldExists, &newExists); err != nil {
			return err
		}
		if oldExists && !newExists {
			if _, err := tx.Exec(ctx, "ALTER INDEX "+pair[0]+" RENAME TO "+pair[1]); err != nil {
				return fmt.Errorf("rename index %s: %w", pair[0], err)
			}
		}
	}
	return nil
}

func (a *app) api(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "API requests must use POST")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if origin != "http://"+r.Host && origin != "https://"+r.Host {
			writeError(w, http.StatusForbidden, "cross-origin requests are not allowed")
			return
		}
	}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	switch r.URL.Path {
	case "/api/casenotes/create":
		a.createCasenote(w, r)
	case "/api/casenotes/update":
		a.updateCasenote(w, r)
	case "/api/casenotes/get":
		a.getCasenote(w, r)
	case "/api/casenotes/search":
		a.searchCasenotes(w, r)
	case "/api/projects/list":
		a.listProjects(w, r)
	case "/api/projects/create":
		a.createProject(w, r)
	case "/api/graph/get":
		a.getGraph(w, r)
	case "/api/comments/create":
		a.createComment(w, r)
	case "/api/links/create":
		a.createLink(w, r)
	case "/api/tags/list":
		a.listTags(w, r)
	case "/api/tags/cloud":
		a.tagCloud(w, r)
	case "/api/health":
		a.health(w, r)
	default:
		writeError(w, http.StatusNotFound, "unknown API endpoint")
	}
}

func (a *app) assets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "page requests must use GET or HEAD")
		return
	}
	// FileServer sanitizes paths; unknown SPA routes get the entry page.
	cleaned := filepath.Clean("/" + r.URL.Path)
	if cleaned != "/" {
		name := filepath.Join(a.static, strings.TrimPrefix(cleaned, "/"))
		if info, err := os.Stat(name); err == nil && !info.IsDir() {
			http.ServeFile(w, r, name)
			return
		}
		if strings.HasPrefix(cleaned, "/assets/") {
			http.NotFound(w, r)
			return
		}
	}
	http.ServeFile(w, r, filepath.Join(a.static, "index.html"))
}

func decodeJSON(r *http.Request, dst any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func badInput(w http.ResponseWriter, err error) {
	writeError(w, http.StatusBadRequest, err.Error())
}
