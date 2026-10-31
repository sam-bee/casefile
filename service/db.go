package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type casenote struct {
	ID             int64           `json:"id"`
	ProjectID      int64           `json:"project_id"`
	Project        string          `json:"project"`
	Title          string          `json:"title"`
	Summary        string          `json:"summary"`
	Body           string          `json:"body"`
	Kind           string          `json:"kind"`
	Status         string          `json:"status"`
	Tags           []string        `json:"tags"`
	Fields         json.RawMessage `json:"fields"`
	Author         string          `json:"author"`
	SourceRevision string          `json:"source_revision"`
	Version        int64           `json:"version"`
	SearchPending  bool            `json:"search_pending"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type comment struct {
	ID         int64     `json:"id"`
	CasenoteID int64     `json:"casenote_id"`
	Author     string    `json:"author"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}

type link struct {
	ID             int64     `json:"id"`
	FromCasenoteID int64     `json:"from_casenote_id"`
	ToCasenoteID   int64     `json:"to_casenote_id"`
	Relation       string    `json:"relation"`
	Weight         float64   `json:"weight"`
	Note           string    `json:"note"`
	Author         string    `json:"author"`
	CreatedAt      time.Time `json:"created_at"`
}

type activity struct {
	ID         int64           `json:"id"`
	CasenoteID int64           `json:"casenote_id"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	Summary    string          `json:"summary"`
	Details    json.RawMessage `json:"details"`
	CreatedAt  time.Time       `json:"created_at"`
}

var actorPattern = regexp.MustCompile(`^[^()]+ \([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\)$`)

func checkActor(value string) (string, error) {
	value, err := checkText("actor", value, 120, true)
	if err != nil {
		return "", err
	}
	if !actorPattern.MatchString(value) {
		return "", errors.New("actor must look like research-agent (provider/model)")
	}
	return value, nil
}

func recordActivity(ctx context.Context, tx pgx.Tx, id int64, actor, action, summary string, details any, originKey string) error {
	encoded, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO casenote_activity (casenote_id,actor,action,summary,details,origin_key)
        VALUES ($1,$2,$3,$4,$5::jsonb,NULLIF($6,''))`, id, actor, action, summary, string(encoded), originKey)
	return err
}

const casenoteColumns = `i.id, i.project_id, p.slug, i.title, i.summary, i.body, i.kind, i.status,
    i.tags, i.fields, i.author, i.source_revision, i.version, i.search_dirty,
    i.created_at, i.updated_at`

type scanner interface{ Scan(...any) error }

func scanCasenote(row scanner) (casenote, error) {
	var result casenote
	err := row.Scan(&result.ID, &result.ProjectID, &result.Project,
		&result.Title, &result.Summary, &result.Body, &result.Kind,
		&result.Status, &result.Tags, &result.Fields, &result.Author, &result.SourceRevision,
		&result.Version, &result.SearchPending, &result.CreatedAt, &result.UpdatedAt)
	if result.Tags == nil {
		result.Tags = []string{}
	}
	if len(result.Fields) == 0 {
		result.Fields = json.RawMessage(`{}`)
	}
	return result, err
}

func (a *app) casenoteByID(ctx context.Context, id int64) (casenote, error) {
	return scanCasenote(a.db.QueryRow(ctx, "SELECT "+casenoteColumns+
		" FROM casenotes AS i JOIN projects AS p ON p.id=i.project_id WHERE i.id=$1", id))
}

func normalizeTags(values []string) ([]string, error) {
	if len(values) > 30 {
		return nil, errors.New("at most 30 tags are allowed")
	}
	unique := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		tag := strings.ToLower(strings.TrimSpace(value))
		if tag == "" {
			continue
		}
		if len(tag) > 80 {
			return nil, errors.New("each tag must be at most 80 bytes")
		}
		if !unique[tag] {
			unique[tag] = true
			result = append(result, tag)
		}
	}
	sort.Strings(result)
	return result, nil
}

func normalizeFields(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("fields must be a JSON object")
	}
	if len(fields) > 30 {
		return nil, errors.New("fields may have at most 30 keys")
	}
	if score, present := fields["human_review_score"]; present {
		var value int
		if err := json.Unmarshal(score, &value); err != nil || value < 0 || value > 5 {
			return nil, errors.New("human_review_score must be an integer from 0 to 5")
		}
	}
	for key := range fields {
		if strings.TrimSpace(key) == "" || len(key) > 80 {
			return nil, errors.New("field names must be 1–80 bytes")
		}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	if len(encoded) > 16384 {
		return nil, errors.New("fields must be at most 16 KiB")
	}
	return encoded, nil
}

func scoreReviewTags(tags []string, fields json.RawMessage) json.RawMessage {
	for _, tag := range tags {
		if tag != "agent refusal" && tag != "working bug report" && tag != "near-miss exploit chain" {
			continue
		}
		var values map[string]json.RawMessage
		_ = json.Unmarshal(fields, &values) // normalizeFields already validated this object.
		values["human_review_score"] = json.RawMessage(`5`)
		encoded, _ := json.Marshal(values)
		return encoded
	}
	return fields
}

func checkText(name, value string, max int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if len(value) > max {
		return "", fmt.Errorf("%s must be at most %d bytes", name, max)
	}
	return value, nil
}

const maxCasenoteSummaryCodePoints = 1400

func checkCasenoteSummary(value string) (string, error) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > maxCasenoteSummaryCodePoints {
		return "", fmt.Errorf("summary must be at most %d Unicode code points", maxCasenoteSummaryCodePoints)
	}
	return value, nil
}

func updatedCasenoteSummary(current string, update *string) (string, error) {
	if update == nil {
		return current, nil
	}
	return checkCasenoteSummary(*update)
}

type createCasenoteInput struct {
	projectSelector
	Title          string          `json:"title"`
	Summary        string          `json:"summary"`
	Body           string          `json:"body"`
	Kind           string          `json:"kind"`
	Status         string          `json:"status"`
	Tags           []string        `json:"tags"`
	Fields         json.RawMessage `json:"fields"`
	Author         string          `json:"author"`
	SourceRevision string          `json:"source_revision"`
}

func validateCreate(input *createCasenoteInput) error {
	var err error
	if input.Title, err = checkText("title", input.Title, 300, true); err != nil {
		return err
	}
	if input.Summary, err = checkCasenoteSummary(input.Summary); err != nil {
		return err
	}
	if input.Body, err = checkText("body", input.Body, 200000, false); err != nil {
		return err
	}
	if input.Author, err = checkActor(input.Author); err != nil {
		return err
	}
	if input.Kind == "" {
		input.Kind = "observation"
	}
	if input.Status == "" {
		input.Status = "needs-work"
	}
	if input.Kind, err = checkText("kind", input.Kind, 80, true); err != nil {
		return err
	}
	if input.Status, err = checkText("status", input.Status, 80, true); err != nil {
		return err
	}
	if input.SourceRevision, err = checkText("source_revision", input.SourceRevision, 120, false); err != nil {
		return err
	}
	input.Tags, err = normalizeTags(input.Tags)
	if err != nil {
		return err
	}
	input.Fields, err = normalizeFields(input.Fields)
	if err != nil {
		return err
	}
	input.Fields = scoreReviewTags(input.Tags, input.Fields)
	input.Fields, err = normalizeFields(input.Fields)
	return err
}

func casenoteSnapshot(title, summary, body, kind, status string, tags []string, fields json.RawMessage, revision string) map[string]any {
	return map[string]any{
		"title": title, "summary": summary, "body": body, "kind": kind, "status": status,
		"tags": tags, "fields": fields, "source_revision": revision,
	}
}

func (a *app) createCasenote(w http.ResponseWriter, r *http.Request) {
	var input createCasenoteInput
	if err := decodeJSON(r, &input); err != nil {
		badInput(w, err)
		return
	}
	if err := validateCreate(&input); err != nil {
		badInput(w, err)
		return
	}
	selected, ok := a.projectForRequest(w, r, input.projectSelector, true)
	if !ok {
		return
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "could not create casenote")
		return
	}
	defer tx.Rollback(r.Context())
	var id int64
	err = tx.QueryRow(r.Context(), `INSERT INTO casenotes
		(project_id, title, summary, body, kind, status, tags, fields, author, source_revision)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10) RETURNING id`, selected.ID,
		input.Title, input.Summary, input.Body, input.Kind, input.Status, input.Tags,
		string(input.Fields), input.Author, input.SourceRevision).Scan(&id)
	if err != nil {
		writeError(w, 500, "could not create casenote")
		log.Printf("create casenote: %v", err)
		return
	}
	if err = recordActivity(r.Context(), tx, id, input.Author, "created", "Casenote created",
		map[string]any{"after": casenoteSnapshot(input.Title, input.Summary, input.Body, input.Kind, input.Status, input.Tags, input.Fields, input.SourceRevision)}, fmt.Sprintf("casenote-%d", id)); err != nil {
		writeError(w, 500, "could not record activity")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "could not create casenote")
		return
	}
	a.tryIndex(r.Context(), id)
	result, err := a.casenoteByID(r.Context(), id)
	if err != nil {
		writeError(w, 500, "could not load created casenote")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"casenote": result})
}

type updateCasenoteInput struct {
	projectSelector
	ID             int64           `json:"id"`
	Version        int64           `json:"version"`
	Actor          string          `json:"actor"`
	Title          *string         `json:"title"`
	Summary        *string         `json:"summary"`
	Body           *string         `json:"body"`
	Kind           *string         `json:"kind"`
	Status         *string         `json:"status"`
	Tags           *[]string       `json:"tags"`
	Fields         json.RawMessage `json:"fields"`
	SourceRevision *string         `json:"source_revision"`
}

func (a *app) updateCasenote(w http.ResponseWriter, r *http.Request) {
	var input updateCasenoteInput
	if err := decodeJSON(r, &input); err != nil {
		badInput(w, err)
		return
	}
	if input.ID <= 0 || input.Version <= 0 {
		writeError(w, 400, "id and version must be positive integers")
		return
	}
	var err error
	input.Actor, err = checkActor(input.Actor)
	if err != nil {
		badInput(w, err)
		return
	}
	current, err := a.casenoteByID(r.Context(), input.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "casenote not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load casenote")
		return
	}
	if input.projectSelector.supplied() {
		selected, ok := a.projectForRequest(w, r, input.projectSelector, false)
		if !ok {
			return
		}
		if selected.ID != current.ProjectID {
			writeError(w, 404, "casenote not found in project")
			return
		}
	}
	before := casenoteSnapshot(current.Title, current.Summary, current.Body, current.Kind, current.Status, current.Tags, current.Fields, current.SourceRevision)
	if input.Title != nil {
		current.Title = *input.Title
	}
	current.Summary, err = updatedCasenoteSummary(current.Summary, input.Summary)
	if err != nil {
		badInput(w, err)
		return
	}
	if input.Body != nil {
		current.Body = *input.Body
	}
	if input.Kind != nil {
		current.Kind = *input.Kind
	}
	if input.Status != nil {
		current.Status = *input.Status
	}
	if input.Tags != nil {
		current.Tags = *input.Tags
	}
	if len(input.Fields) > 0 {
		current.Fields = input.Fields
	}
	if input.SourceRevision != nil {
		current.SourceRevision = *input.SourceRevision
	}
	changed := createCasenoteInput{Title: current.Title, Summary: current.Summary, Body: current.Body, Kind: current.Kind,
		Status: current.Status, Tags: current.Tags, Fields: current.Fields, Author: current.Author,
		SourceRevision: current.SourceRevision}
	if err := validateCreate(&changed); err != nil {
		badInput(w, err)
		return
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "could not update casenote")
		return
	}
	defer tx.Rollback(r.Context())
	var updatedID int64
	err = tx.QueryRow(r.Context(), `UPDATE casenotes SET
		title=$1, summary=$2, body=$3, kind=$4, status=$5, tags=$6, fields=$7::jsonb, source_revision=$8,
		version=version+1, search_dirty=TRUE, updated_at=now()
		WHERE id=$9 AND version=$10 RETURNING id`,
		changed.Title, changed.Summary, changed.Body, changed.Kind, changed.Status, changed.Tags,
		string(changed.Fields), changed.SourceRevision, input.ID, input.Version).Scan(&updatedID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 409, "casenote changed; reload and retry")
		return
	}
	if err != nil {
		writeError(w, 500, "could not update casenote")
		log.Printf("update casenote: %v", err)
		return
	}
	if err = recordActivity(r.Context(), tx, updatedID, input.Actor, "updated", "Casenote edited",
		map[string]any{"version": input.Version + 1, "before": before,
			"after": casenoteSnapshot(changed.Title, changed.Summary, changed.Body, changed.Kind, changed.Status, changed.Tags, changed.Fields, changed.SourceRevision)}, ""); err != nil {
		writeError(w, 500, "could not record activity")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "could not update casenote")
		return
	}
	a.tryIndex(r.Context(), updatedID)
	result, err := a.casenoteByID(r.Context(), updatedID)
	if err != nil {
		writeError(w, 500, "could not load updated casenote")
		return
	}
	writeJSON(w, 200, map[string]any{"casenote": result})
}

func (a *app) getCasenote(w http.ResponseWriter, r *http.Request) {
	var input struct {
		projectSelector
		ID int64 `json:"id"`
	}
	if err := decodeJSON(r, &input); err != nil {
		badInput(w, err)
		return
	}
	if input.ID <= 0 {
		writeError(w, 400, "id must be a positive integer")
		return
	}
	result, err := a.casenoteByID(r.Context(), input.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "casenote not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load casenote")
		return
	}
	if input.projectSelector.supplied() {
		selected, ok := a.projectForRequest(w, r, input.projectSelector, false)
		if !ok {
			return
		}
		if selected.ID != result.ProjectID {
			writeError(w, 404, "casenote not found in project")
			return
		}
	}

	casenote_comments := []comment{}
	rows, err := a.db.Query(r.Context(), `SELECT id, casenote_id, author, body, created_at
        FROM casenote_comments WHERE casenote_id=$1 ORDER BY created_at, id`, input.ID)
	if err != nil {
		writeError(w, 500, "could not load casenote_comments")
		return
	}
	for rows.Next() {
		var one comment
		if err := rows.Scan(&one.ID, &one.CasenoteID, &one.Author, &one.Body, &one.CreatedAt); err != nil {
			rows.Close()
			writeError(w, 500, "could not load casenote_comments")
			return
		}
		casenote_comments = append(casenote_comments, one)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, 500, "could not load casenote_comments")
		return
	}

	casenote_links := []link{}
	rows, err = a.db.Query(r.Context(), `SELECT id, from_casenote_id, to_casenote_id, relation,
        weight, note, author, created_at FROM casenote_links
        WHERE from_casenote_id=$1 OR to_casenote_id=$1 ORDER BY created_at, id`, input.ID)
	if err != nil {
		writeError(w, 500, "could not load casenote_links")
		return
	}
	for rows.Next() {
		var one link
		if err := rows.Scan(&one.ID, &one.FromCasenoteID, &one.ToCasenoteID, &one.Relation,
			&one.Weight, &one.Note, &one.Author, &one.CreatedAt); err != nil {
			rows.Close()
			writeError(w, 500, "could not load casenote_links")
			return
		}
		casenote_links = append(casenote_links, one)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, 500, "could not load casenote_links")
		return
	}
	activities := []activity{}
	rows, err = a.db.Query(r.Context(), `SELECT id, casenote_id, actor, action, summary, details, created_at
        FROM casenote_activity WHERE casenote_id=$1 ORDER BY created_at, id`, input.ID)
	if err != nil {
		writeError(w, 500, "could not load activity")
		return
	}
	for rows.Next() {
		var one activity
		if err := rows.Scan(&one.ID, &one.CasenoteID, &one.Actor, &one.Action, &one.Summary, &one.Details, &one.CreatedAt); err != nil {
			rows.Close()
			writeError(w, 500, "could not load activity")
			return
		}
		activities = append(activities, one)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, 500, "could not load activity")
		return
	}
	writeJSON(w, 200, map[string]any{"casenote": result, "comments": casenote_comments, "links": casenote_links, "activity": activities})
}

func (a *app) createComment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		projectSelector
		CasenoteID int64  `json:"casenote_id"`
		Author     string `json:"author"`
		Body       string `json:"body"`
	}
	if err := decodeJSON(r, &input); err != nil {
		badInput(w, err)
		return
	}
	var err error
	if input.CasenoteID <= 0 {
		writeError(w, 400, "casenote_id must be a positive integer")
		return
	}
	if input.Author, err = checkActor(input.Author); err != nil {
		badInput(w, err)
		return
	}
	if input.Body, err = checkText("body", input.Body, 50000, true); err != nil {
		badInput(w, err)
		return
	}
	if input.projectSelector.supplied() {
		selected, ok := a.projectForRequest(w, r, input.projectSelector, false)
		if !ok {
			return
		}
		current, err := a.casenoteByID(r.Context(), input.CasenoteID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && current.ProjectID != selected.ID) {
			writeError(w, 404, "casenote not found in project")
			return
		}
		if err != nil {
			writeError(w, 500, "could not load casenote")
			return
		}
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "could not create comment")
		return
	}
	defer tx.Rollback(r.Context())
	var result comment
	err = tx.QueryRow(r.Context(), `INSERT INTO casenote_comments (casenote_id,author,body)
        VALUES ($1,$2,$3) RETURNING id,casenote_id,author,body,created_at`,
		input.CasenoteID, input.Author, input.Body).Scan(&result.ID, &result.CasenoteID,
		&result.Author, &result.Body, &result.CreatedAt)
	if isForeignKey(err) {
		writeError(w, 404, "casenote not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not create comment")
		log.Printf("create comment: %v", err)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE casenotes SET version=version+1,
        search_dirty=TRUE, updated_at=now() WHERE id=$1`, input.CasenoteID); err != nil {
		writeError(w, 500, "could not update casenote")
		return
	}
	if err = recordActivity(r.Context(), tx, input.CasenoteID, input.Author, "commented", "Comment added", map[string]any{"comment_id": result.ID, "body": result.Body}, fmt.Sprintf("comment-%d", result.ID)); err != nil {
		writeError(w, 500, "could not record activity")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "could not create comment")
		return
	}
	a.tryIndex(r.Context(), input.CasenoteID)
	writeJSON(w, http.StatusCreated, map[string]any{"comment": result})
}

func (a *app) createLink(w http.ResponseWriter, r *http.Request) {
	var input struct {
		projectSelector
		FromCasenoteID int64    `json:"from_casenote_id"`
		ToCasenoteID   int64    `json:"to_casenote_id"`
		Relation       string   `json:"relation"`
		Weight         *float64 `json:"weight"`
		Note           string   `json:"note"`
		Author         string   `json:"author"`
	}
	if err := decodeJSON(r, &input); err != nil {
		badInput(w, err)
		return
	}
	if input.FromCasenoteID <= 0 || input.ToCasenoteID <= 0 || input.FromCasenoteID == input.ToCasenoteID {
		writeError(w, 400, "link requires two different positive casenote IDs")
		return
	}
	var err error
	if input.Relation, err = checkText("relation", input.Relation, 80, true); err != nil {
		badInput(w, err)
		return
	}
	if input.Note, err = checkText("note", input.Note, 5000, false); err != nil {
		badInput(w, err)
		return
	}
	if input.Author, err = checkActor(input.Author); err != nil {
		badInput(w, err)
		return
	}
	if input.Weight == nil {
		writeError(w, 400, "weight is required")
		return
	}
	if *input.Weight < 0 || *input.Weight > 1 {
		writeError(w, 400, "weight must be between 0 and 1")
		return
	}
	from, err := a.casenoteByID(r.Context(), input.FromCasenoteID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "linked casenote not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load linked casenote")
		return
	}
	to, err := a.casenoteByID(r.Context(), input.ToCasenoteID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "linked casenote not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load linked casenote")
		return
	}
	if from.ProjectID != to.ProjectID {
		writeError(w, 400, "linked casenotes must belong to the same project")
		return
	}
	if input.projectSelector.supplied() {
		selected, ok := a.projectForRequest(w, r, input.projectSelector, false)
		if !ok {
			return
		}
		if selected.ID != from.ProjectID {
			writeError(w, 404, "linked casenotes not found in project")
			return
		}
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "could not create link")
		return
	}
	defer tx.Rollback(r.Context())
	var result link
	err = tx.QueryRow(r.Context(), `INSERT INTO casenote_links
        (project_id,from_casenote_id,to_casenote_id,relation,weight,note,author)
        VALUES ($1,$2,$3,$4,$5,$6,$7)
        RETURNING id,from_casenote_id,to_casenote_id,relation,weight,note,author,created_at`,
		from.ProjectID, input.FromCasenoteID, input.ToCasenoteID, input.Relation, *input.Weight,
		input.Note, input.Author).Scan(&result.ID, &result.FromCasenoteID,
		&result.ToCasenoteID, &result.Relation, &result.Weight, &result.Note,
		&result.Author, &result.CreatedAt)
	if isForeignKey(err) {
		writeError(w, 404, "linked casenote not found")
		return
	}
	if isUnique(err) {
		writeError(w, 409, "this link already exists")
		return
	}
	if err != nil {
		writeError(w, 500, "could not create link")
		log.Printf("create link: %v", err)
		return
	}
	for _, end := range []struct {
		id, other         int64
		direction, suffix string
	}{
		{input.FromCasenoteID, input.ToCasenoteID, "outgoing", "from"},
		{input.ToCasenoteID, input.FromCasenoteID, "incoming", "to"},
	} {
		if err = recordActivity(r.Context(), tx, end.id, input.Author, "linked", "Casenote linked", map[string]any{"link_id": result.ID, "other_casenote_id": end.other, "direction": end.direction, "relation": input.Relation, "weight": input.Weight, "note": input.Note}, fmt.Sprintf("link-%d-%s", result.ID, end.suffix)); err != nil {
			writeError(w, 500, "could not record activity")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `UPDATE casenotes SET version=version+1, search_dirty=TRUE, updated_at=now()
        WHERE id IN ($1,$2)`, input.FromCasenoteID, input.ToCasenoteID); err != nil {
		writeError(w, 500, "could not update linked casenotes")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "could not create link")
		return
	}
	a.tryIndex(r.Context(), input.FromCasenoteID)
	a.tryIndex(r.Context(), input.ToCasenoteID)
	writeJSON(w, http.StatusCreated, map[string]any{"link": result})
}

func isForeignKey(err error) bool {
	var pgerr *pgconn.PgError
	return errors.As(err, &pgerr) && pgerr.Code == "23503"
}

func isUnique(err error) bool {
	var pgerr *pgconn.PgError
	return errors.As(err, &pgerr) && pgerr.Code == "23505"
}

func (a *app) listTags(w http.ResponseWriter, r *http.Request) {
	var input struct {
		projectSelector
		Query string `json:"query"`
	}
	if err := decodeJSON(r, &input); err != nil {
		badInput(w, err)
		return
	}
	if len(input.Query) > 100 {
		writeError(w, 400, "query is too long")
		return
	}
	selected, ok := a.projectForRequest(w, r, input.projectSelector, true)
	if !ok {
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT DISTINCT tag FROM casenotes,
        unnest(tags) AS tag WHERE project_id=$1 AND strpos(tag, lower($2)) > 0
        ORDER BY tag LIMIT 100`, selected.ID, strings.TrimSpace(input.Query))
	if err != nil {
		writeError(w, 500, "could not list tags")
		return
	}
	tags := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			rows.Close()
			writeError(w, 500, "could not list tags")
			return
		}
		tags = append(tags, tag)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, 500, "could not list tags")
		return
	}
	writeJSON(w, 200, map[string]any{"tags": tags})
}

func (a *app) tagCloud(w http.ResponseWriter, r *http.Request) {
	var input struct{ projectSelector }
	if err := decodeJSON(r, &input); err != nil {
		badInput(w, err)
		return
	}
	selected, ok := a.projectForRequest(w, r, input.projectSelector, true)
	if !ok {
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT tag, count(*) FROM casenotes,
		unnest(tags) AS tag WHERE project_id=$1 GROUP BY tag
		ORDER BY count(*) DESC, tag LIMIT 100`, selected.ID)
	if err != nil {
		writeError(w, 500, "could not load tag cloud")
		return
	}
	tags := []map[string]any{}
	for rows.Next() {
		var tag string
		var count int64
		if err := rows.Scan(&tag, &count); err != nil {
			rows.Close()
			writeError(w, 500, "could not load tag cloud")
			return
		}
		tags = append(tags, map[string]any{"tag": tag, "count": count})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, 500, "could not load tag cloud")
		return
	}
	writeJSON(w, 200, map[string]any{"tags": tags})
}

func (a *app) health(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if err := decodeJSON(r, &input); err != nil {
		badInput(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.db.Ping(ctx); err != nil {
		writeError(w, 503, "PostgreSQL unavailable")
		return
	}
	if err := a.search.ping(ctx); err != nil {
		writeError(w, 503, "Elasticsearch unavailable")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *app) tryIndex(parent context.Context, id int64) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	needsReindex, err := a.search.ensureIndex(ctx)
	if err != nil {
		log.Printf("search index unavailable; casenote %d queued: %v", id, err)
		return
	}
	if needsReindex {
		if _, err := a.db.Exec(ctx, `UPDATE casenotes SET search_dirty=TRUE`); err != nil {
			log.Printf("mark casenotes for indexing: %v", err)
		}
	}
	if err := a.indexOne(ctx, id); err != nil {
		log.Printf("index casenote %d deferred: %v", id, err)
	}
}
