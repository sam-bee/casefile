package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const defaultProjectSlug = "local-research"

var projectSlugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

type project struct {
	ID          int64     `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

type projectSelector struct {
	ProjectID int64  `json:"project_id"`
	Project   string `json:"project"`
}

type projectInputError string

func (e projectInputError) Error() string { return string(e) }

func scanProject(row scanner) (project, error) {
	var result project
	err := row.Scan(&result.ID, &result.Slug, &result.Name, &result.Description, &result.CreatedAt)
	return result, err
}

func (a *app) resolveProject(ctx context.Context, selector projectSelector, useDefault bool) (project, error) {
	if selector.ProjectID < 0 {
		return project{}, projectInputError("project_id must be a positive integer")
	}
	slug := strings.TrimSpace(selector.Project)
	if slug != "" && !projectSlugPattern.MatchString(slug) {
		return project{}, projectInputError("project must be a lowercase project slug")
	}
	if selector.ProjectID == 0 && slug == "" {
		if !useDefault {
			return project{}, nil
		}
		slug = defaultProjectSlug
	}
	var result project
	var err error
	if selector.ProjectID > 0 {
		result, err = scanProject(a.db.QueryRow(ctx, `SELECT id, slug, name, description, created_at
            FROM projects WHERE id=$1`, selector.ProjectID))
	} else {
		result, err = scanProject(a.db.QueryRow(ctx, `SELECT id, slug, name, description, created_at
            FROM projects WHERE slug=$1`, slug))
	}
	if err == nil && slug != "" && result.Slug != slug {
		return project{}, projectInputError("project and project_id refer to different projects")
	}
	return result, err
}

func (a *app) projectForRequest(w http.ResponseWriter, r *http.Request, selector projectSelector, useDefault bool) (project, bool) {
	result, err := a.resolveProject(r.Context(), selector, useDefault)
	if err == nil {
		return result, true
	}
	var inputErr projectInputError
	switch {
	case errors.As(err, &inputErr):
		writeError(w, 400, err.Error())
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, 404, "project not found")
	default:
		log.Printf("resolve project: %v", err)
		writeError(w, 500, "could not load project")
	}
	return project{}, false
}

func (selector projectSelector) supplied() bool {
	return selector.ProjectID != 0 || strings.TrimSpace(selector.Project) != ""
}

func (a *app) listProjects(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if err := decodeJSON(r, &input); err != nil {
		badInput(w, err)
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT id, slug, name, description, created_at
        FROM projects ORDER BY lower(name), id`)
	if err != nil {
		writeError(w, 500, "could not list projects")
		return
	}
	projects := []project{}
	for rows.Next() {
		one, err := scanProject(rows)
		if err != nil {
			rows.Close()
			writeError(w, 500, "could not list projects")
			return
		}
		projects = append(projects, one)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, 500, "could not list projects")
		return
	}
	writeJSON(w, 200, map[string]any{"projects": projects})
}

func slugFromName(name string) string {
	var builder strings.Builder
	previousDash := false
	for _, character := range strings.ToLower(name) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			builder.WriteRune(character)
			previousDash = false
		} else if builder.Len() > 0 && !previousDash {
			builder.WriteByte('-')
			previousDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func (a *app) createProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
	}
	if err := decodeJSON(r, &input); err != nil {
		badInput(w, err)
		return
	}
	var err error
	if input.Name, err = checkText("name", input.Name, 120, true); err != nil {
		badInput(w, err)
		return
	}
	if input.Description, err = checkText("description", input.Description, 500, false); err != nil {
		badInput(w, err)
		return
	}
	input.Slug = strings.ToLower(strings.TrimSpace(input.Slug))
	if input.Slug == "" {
		input.Slug = slugFromName(input.Name)
	}
	if !projectSlugPattern.MatchString(input.Slug) {
		badInput(w, fmt.Errorf("slug must start with a letter and contain only lowercase letters, numbers, or hyphens (at most 64 characters)"))
		return
	}
	result, err := scanProject(a.db.QueryRow(r.Context(), `INSERT INTO projects
        (slug, name, description) VALUES ($1,$2,$3)
        RETURNING id, slug, name, description, created_at`, input.Slug, input.Name, input.Description))
	if isUnique(err) {
		writeError(w, 409, "project slug already exists")
		return
	}
	if err != nil {
		log.Printf("create project: %v", err)
		writeError(w, 500, "could not create project")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": result})
}

type graphNode struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Tags      []string `json:"tags"`
	ProjectID int64    `json:"project_id"`
	Project   string   `json:"project"`
}

type graphEdge struct {
	ID             int64   `json:"id"`
	FromCasenoteID int64   `json:"from_casenote_id"`
	ToCasenoteID   int64   `json:"to_casenote_id"`
	Relation       string  `json:"relation"`
	Weight         float64 `json:"weight"`
	Note           string  `json:"note"`
}

func (a *app) getGraph(w http.ResponseWriter, r *http.Request) {
	var input struct {
		projectSelector
		Limit int `json:"limit"`
	}
	if err := decodeJSON(r, &input); err != nil {
		badInput(w, err)
		return
	}
	if input.Limit == 0 {
		input.Limit = 200
	}
	if input.Limit < 1 || input.Limit > 500 {
		writeError(w, 400, "limit must be between 1 and 500")
		return
	}
	selected, ok := a.projectForRequest(w, r, input.projectSelector, true)
	if !ok {
		return
	}
	var totalNodes int64
	if err := a.db.QueryRow(r.Context(), `SELECT count(*) FROM casenotes WHERE project_id=$1`, selected.ID).Scan(&totalNodes); err != nil {
		writeError(w, 500, "could not count graph nodes")
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT id, title, kind, status, tags FROM casenotes
        WHERE project_id=$1 ORDER BY updated_at DESC, id DESC LIMIT $2`, selected.ID, input.Limit)
	if err != nil {
		writeError(w, 500, "could not load graph nodes")
		return
	}
	nodes := []graphNode{}
	ids := []int64{}
	for rows.Next() {
		one := graphNode{ProjectID: selected.ID, Project: selected.Slug}
		if err := rows.Scan(&one.ID, &one.Title, &one.Kind, &one.Status, &one.Tags); err != nil {
			rows.Close()
			writeError(w, 500, "could not load graph nodes")
			return
		}
		if one.Tags == nil {
			one.Tags = []string{}
		}
		nodes = append(nodes, one)
		ids = append(ids, one.ID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, 500, "could not load graph nodes")
		return
	}
	edges := []graphEdge{}
	truncatedEdges := false
	if len(ids) > 0 {
		rows, err = a.db.Query(r.Context(), `SELECT id, from_casenote_id, to_casenote_id, relation, weight, note
            FROM casenote_links WHERE project_id=$1 AND from_casenote_id=ANY($2::bigint[])
            AND to_casenote_id=ANY($2::bigint[]) ORDER BY id LIMIT 3001`, selected.ID, ids)
		if err != nil {
			writeError(w, 500, "could not load graph casenote_links")
			return
		}
		for rows.Next() {
			var one graphEdge
			if err := rows.Scan(&one.ID, &one.FromCasenoteID, &one.ToCasenoteID, &one.Relation, &one.Weight, &one.Note); err != nil {
				rows.Close()
				writeError(w, 500, "could not load graph casenote_links")
				return
			}
			if len(edges) == 3000 {
				truncatedEdges = true
				break
			}
			edges = append(edges, one)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			writeError(w, 500, "could not load graph casenote_links")
			return
		}
	}
	writeJSON(w, 200, map[string]any{
		"project": selected, "nodes": nodes, "edges": edges,
		"total_nodes": totalNodes, "truncated_edges": truncatedEdges,
		"truncated": totalNodes > int64(input.Limit) || truncatedEdges,
	})
}
