package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

const searchIndex = "casefile-casenotes"

type searchClient struct {
	base               string
	client             *http.Client
	replicasConfigured atomic.Bool
	mappingReady       atomic.Bool
}

func newSearchClient(base string) *searchClient {
	return &searchClient{
		base:   strings.TrimRight(base, "/"),
		client: &http.Client{Timeout: 8 * time.Second},
	}
}

func (s *searchClient) request(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return s.client.Do(req)
}

func closeResponse(response *http.Response) {
	if response != nil {
		io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
	}
}

func (s *searchClient) ping(ctx context.Context) error {
	response, err := s.request(ctx, http.MethodHead, "/"+searchIndex, nil)
	if err != nil {
		return err
	}
	status := response.StatusCode
	closeResponse(response)
	if status != 200 {
		return fmt.Errorf("Elasticsearch index status %d", status)
	}
	response, err = s.request(ctx, http.MethodGet,
		"/_cluster/health/"+searchIndex+"?wait_for_status=yellow&timeout=1s", nil)
	if err != nil {
		return err
	}
	defer closeResponse(response)
	if response.StatusCode != 200 {
		return fmt.Errorf("Elasticsearch index health HTTP %d", response.StatusCode)
	}
	var health struct {
		Status              string `json:"status"`
		TimedOut            bool   `json:"timed_out"`
		ActivePrimaryShards int    `json:"active_primary_shards"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&health); err != nil {
		return err
	}
	if health.TimedOut || health.ActivePrimaryShards == 0 ||
		(health.Status != "yellow" && health.Status != "green") {
		return fmt.Errorf("Elasticsearch index shards unavailable (status %q)", health.Status)
	}
	return nil
}

func (s *searchClient) ensureSingleNodeSettings(ctx context.Context) error {
	if s.replicasConfigured.Load() {
		return nil
	}
	response, err := s.request(ctx, http.MethodPut, "/"+searchIndex+"/_settings",
		map[string]any{"index": map[string]any{"number_of_replicas": 0}})
	if err != nil {
		return err
	}
	defer closeResponse(response)
	if response.StatusCode != 200 {
		return fmt.Errorf("set Elasticsearch replicas: HTTP %d", response.StatusCode)
	}
	s.replicasConfigured.Store(true)
	return nil
}

// ensureSearchMapping reports whether indexed documents are missing numeric scores.
func (s *searchClient) ensureSearchMapping(ctx context.Context) (bool, error) {
	if s.mappingReady.Load() {
		return false, nil
	}
	response, err := s.request(ctx, http.MethodPut, "/"+searchIndex+"/_mapping",
		map[string]any{"properties": map[string]any{
			"project_id":         map[string]any{"type": "long"},
			"project":            map[string]any{"type": "keyword"},
			"summary":            map[string]any{"type": "text"},
			"fields_text":        map[string]any{"type": "text"},
			"human_review_score": map[string]any{"type": "byte"},
		}})
	if err != nil {
		return false, err
	}
	defer closeResponse(response)
	if response.StatusCode != 200 {
		return false, fmt.Errorf("set Elasticsearch project mapping: HTTP %d", response.StatusCode)
	}
	countResponse, err := s.request(ctx, http.MethodPost, "/"+searchIndex+"/_count",
		map[string]any{"query": map[string]any{"bool": map[string]any{
			"must_not": map[string]any{"exists": map[string]any{"field": "human_review_score"}},
		}}})
	if err != nil {
		return false, err
	}
	defer closeResponse(countResponse)
	if countResponse.StatusCode != 200 {
		return false, fmt.Errorf("count unscored Elasticsearch documents: HTTP %d", countResponse.StatusCode)
	}
	var missing struct {
		Count int64 `json:"count"`
	}
	if err := json.NewDecoder(io.LimitReader(countResponse.Body, 1<<20)).Decode(&missing); err != nil {
		return false, err
	}
	s.mappingReady.Store(true)
	return missing.Count > 0, nil
}

func (s *searchClient) ensureExistingIndex(ctx context.Context) (bool, error) {
	if err := s.ensureSingleNodeSettings(ctx); err != nil {
		return false, err
	}
	return s.ensureSearchMapping(ctx)
}

func (s *searchClient) ensureIndex(ctx context.Context) (bool, error) {
	response, err := s.request(ctx, http.MethodHead, "/"+searchIndex, nil)
	if err != nil {
		return false, err
	}
	status := response.StatusCode
	closeResponse(response)
	if status == 200 {
		return s.ensureExistingIndex(ctx)
	}
	if status != 404 {
		return false, fmt.Errorf("check Elasticsearch index: HTTP %d", status)
	}
	mapping := map[string]any{
		"settings": map[string]any{"number_of_replicas": 0},
		"mappings": map[string]any{
			"dynamic": false,
			"properties": map[string]any{
				"id":                 map[string]any{"type": "long"},
				"project_id":         map[string]any{"type": "long"},
				"project":            map[string]any{"type": "keyword"},
				"title":              map[string]any{"type": "text"},
				"summary":            map[string]any{"type": "text"},
				"body":               map[string]any{"type": "text"},
				"comments":           map[string]any{"type": "text"},
				"activity":           map[string]any{"type": "text"},
				"fields_text":        map[string]any{"type": "text"},
				"human_review_score": map[string]any{"type": "byte"},
				"tags":               map[string]any{"type": "keyword"},
				"kind":               map[string]any{"type": "keyword"},
				"status":             map[string]any{"type": "keyword"},
				"source_revision":    map[string]any{"type": "keyword"},
				"updated_at":         map[string]any{"type": "date"},
			},
		},
	}
	response, err = s.request(ctx, http.MethodPut, "/"+searchIndex, mapping)
	if err != nil {
		return false, err
	}
	defer closeResponse(response)
	if response.StatusCode == 400 {
		// A second instance may have created the index after our existence check.
		check, err := s.request(ctx, http.MethodHead, "/"+searchIndex, nil)
		if err != nil {
			return false, err
		}
		defer closeResponse(check)
		if check.StatusCode == 200 {
			return s.ensureExistingIndex(ctx)
		}
	}
	if response.StatusCode != 200 {
		return false, fmt.Errorf("create Elasticsearch index: HTTP %d", response.StatusCode)
	}
	s.replicasConfigured.Store(true)
	s.mappingReady.Store(true)
	return true, nil
}

func (a *app) indexLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		needsReindex, err := a.search.ensureIndex(ctx)
		if err != nil {
			log.Printf("Elasticsearch index unavailable: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		if needsReindex {
			if _, err := a.db.Exec(ctx, `UPDATE casenotes SET search_dirty=TRUE`); err != nil {
				log.Printf("mark casenotes for indexing: %v", err)
			}
		}
		rows, err := a.db.Query(ctx, `SELECT id FROM casenotes WHERE search_dirty ORDER BY id LIMIT 100`)
		if err != nil {
			log.Printf("load pending search casenotes: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				log.Printf("scan pending casenote: %v", err)
				break
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			log.Printf("pending casenote query: %v", err)
		}
		for _, id := range ids {
			if err := a.indexOne(ctx, id); err != nil {
				log.Printf("index pending casenote %d: %v", id, err)
			}
		}
		time.Sleep(3 * time.Second)
	}
}

func (a *app) indexOne(ctx context.Context, id int64) error {
	current, err := a.casenoteByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var commentText string
	err = a.db.QueryRow(ctx, `SELECT COALESCE(string_agg(body, ' ' ORDER BY id), '')
        FROM casenote_comments WHERE casenote_id=$1`, id).Scan(&commentText)
	if err != nil {
		return err
	}
	var activityText string
	err = a.db.QueryRow(ctx, `SELECT COALESCE(string_agg(summary, ' ' ORDER BY id), '')
        FROM casenote_activity WHERE casenote_id=$1`, id).Scan(&activityText)
	if err != nil {
		return err
	}
	doc := map[string]any{
		"id": current.ID, "project_id": current.ProjectID, "project": current.Project,
		"title": current.Title, "summary": current.Summary, "body": current.Body,
		"comments": commentText, "activity": activityText, "fields_text": string(current.Fields), "human_review_score": reviewScoreFromFields(current.Fields),
		"tags": current.Tags, "kind": current.Kind,
		"status": current.Status, "source_revision": current.SourceRevision,
		"updated_at": current.UpdatedAt.Format(time.RFC3339Nano),
	}
	path := "/" + searchIndex + "/_doc/" + strconv.FormatInt(id, 10) +
		"?version=" + strconv.FormatInt(current.Version, 10) +
		"&version_type=external_gte&refresh=wait_for"
	response, err := a.search.request(ctx, http.MethodPut, path, doc)
	if err != nil {
		return err
	}
	defer closeResponse(response)
	if response.StatusCode != 200 && response.StatusCode != 201 {
		return fmt.Errorf("index casenote: HTTP %d", response.StatusCode)
	}
	_, err = a.db.Exec(ctx, `UPDATE casenotes SET search_dirty=FALSE
        WHERE id=$1 AND version=$2`, id, current.Version)
	return err
}

type searchInput struct {
	projectSelector
	Query          string   `json:"query"`
	Tags           []string `json:"tags"`
	Kind           string   `json:"kind"`
	Status         string   `json:"status"`
	ReviewScore    *int     `json:"review_score"`
	ReviewOperator string   `json:"review_operator"`
	Limit          int      `json:"limit"`
	Offset         int      `json:"offset"`
}

func reviewScoreFromFields(raw json.RawMessage) int {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return 0
	}
	var score int
	if json.Unmarshal(fields["human_review_score"], &score) != nil || score < 0 || score > 5 {
		return 0
	}
	return score
}

func (input searchInput) validateReviewFilter() error {
	if input.ReviewScore == nil {
		if input.ReviewOperator != "" {
			return errors.New("review_operator requires review_score")
		}
		return nil
	}
	if *input.ReviewScore < 1 || *input.ReviewScore > 5 {
		return errors.New("review_score must be an integer from 1 to 5")
	}
	if input.ReviewOperator != "eq" && input.ReviewOperator != "gte" {
		return errors.New("review_operator must be eq or gte")
	}
	return nil
}

func (a *app) searchCasenotes(w http.ResponseWriter, r *http.Request) {
	var input searchInput
	if err := decodeJSON(r, &input); err != nil {
		badInput(w, err)
		return
	}
	input.Query = strings.TrimSpace(input.Query)
	if len(input.Query) > 500 {
		writeError(w, 400, "query is too long")
		return
	}
	if len(input.Kind) > 80 || len(input.Status) > 80 {
		writeError(w, 400, "kind or status is too long")
		return
	}
	if err := input.validateReviewFilter(); err != nil {
		badInput(w, err)
		return
	}
	tags, err := normalizeTags(input.Tags)
	if err != nil {
		badInput(w, err)
		return
	}
	input.Tags = tags
	selected, ok := a.projectForRequest(w, r, input.projectSelector, true)
	if !ok {
		return
	}
	input.ProjectID = selected.ID
	input.Project = selected.Slug
	if input.Limit == 0 {
		input.Limit = 20
	}
	if input.Limit < 0 || input.Limit > 100 || input.Offset < 0 || input.Offset+input.Limit > 10000 {
		writeError(w, 400, "limit must be 1–100 and offset plus limit at most 10000")
		return
	}
	ids, total, err := a.searchCasenotesInElastic(r.Context(), input)
	if err != nil {
		log.Printf("search casenotes: %v", err)
		writeError(w, 503, "search is temporarily unavailable")
		return
	}
	casenotes := make([]casenote, 0, len(ids))
	if len(ids) > 0 {
		rows, err := a.db.Query(r.Context(), "SELECT "+casenoteColumns+
			" FROM casenotes AS i JOIN projects AS p ON p.id=i.project_id"+
			" WHERE i.id = ANY($1::bigint[]) AND i.project_id=$2", ids, selected.ID)
		if err != nil {
			writeError(w, 500, "could not load search results")
			return
		}
		byID := make(map[int64]casenote, len(ids))
		for rows.Next() {
			found, err := scanCasenote(rows)
			if err != nil {
				rows.Close()
				writeError(w, 500, "could not load search results")
				return
			}
			byID[found.ID] = found
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			writeError(w, 500, "could not load search results")
			return
		}
		for _, id := range ids {
			if found, ok := byID[id]; ok {
				casenotes = append(casenotes, found)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"casenotes": casenotes, "total": total})
}

func (a *app) searchCasenotesInElastic(ctx context.Context, input searchInput) ([]int64, int64, error) {
	filters := []any{map[string]any{"term": map[string]any{"project_id": input.ProjectID}}}
	if input.Kind != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"kind": input.Kind}})
	}
	if input.Status != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"status": input.Status}})
	}
	if input.ReviewScore != nil {
		if input.ReviewOperator == "eq" {
			filters = append(filters, map[string]any{"term": map[string]any{"human_review_score": *input.ReviewScore}})
		} else {
			filters = append(filters, map[string]any{"range": map[string]any{"human_review_score": map[string]any{"gte": *input.ReviewScore}}})
		}
	}
	for _, tag := range input.Tags {
		filters = append(filters, map[string]any{"term": map[string]any{"tags": tag}})
	}
	must := []any{}
	if input.Query != "" {
		must = append(must, map[string]any{"multi_match": map[string]any{
			"query": input.Query, "fields": []string{"title^3", "summary^2", "body", "comments", "activity", "fields_text"},
			"type": "best_fields", "operator": "and",
		}})
	} else {
		must = append(must, map[string]any{"match_all": map[string]any{}})
	}
	sort := []any{map[string]any{"updated_at": map[string]any{"order": "desc"}}}
	if input.Query != "" {
		sort = append([]any{map[string]any{"_score": map[string]any{"order": "desc"}}}, sort...)
	}
	query := map[string]any{
		"from": input.Offset, "size": input.Limit, "track_total_hits": true,
		"_source": false,
		"query":   map[string]any{"bool": map[string]any{"must": must, "filter": filters}},
		"sort":    sort,
	}
	response, err := a.search.request(ctx, http.MethodPost, "/"+searchIndex+"/_search", query)
	if err != nil {
		return nil, 0, err
	}
	defer closeResponse(response)
	if response.StatusCode != 200 {
		return nil, 0, fmt.Errorf("Elasticsearch search: HTTP %d", response.StatusCode)
	}
	var result struct {
		Hits struct {
			Total struct {
				Value int64 `json:"value"`
			} `json:"total"`
			Hits []struct {
				ID string `json:"_id"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&result); err != nil {
		return nil, 0, err
	}
	ids := make([]int64, 0, len(result.Hits.Hits))
	for _, hit := range result.Hits.Hits {
		id, err := strconv.ParseInt(hit.ID, 10, 64)
		if err != nil {
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	return ids, result.Hits.Total.Value, nil
}
