package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeTagsKeepsFreeTextAndDeduplicates(t *testing.T) {
	got, err := normalizeTags([]string{"  AuthZ ", "Negative Finding", "authz", "  "})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"authz", "negative finding"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestOptionalFieldsRequireJSONObject(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `"score"`} {
		if _, err := normalizeFields(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted non-object fields %s", raw)
		}
	}
	got, err := normalizeFields(json.RawMessage(`{"cvss_score":7.5,"affected_version":"1.2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(got) || !strings.Contains(string(got), `"cvss_score":7.5`) {
		t.Fatalf("unexpected fields %s", got)
	}
}

func TestCasenoteSummaryUsesTrimmedUnicodeCodePointLimit(t *testing.T) {
	valid := strings.Repeat("界", maxCasenoteSummaryCodePoints) + "  "
	got, err := checkCasenoteSummary(valid)
	if err != nil {
		t.Fatalf("accepted summary at limit: %v", err)
	}
	if got != strings.Repeat("界", maxCasenoteSummaryCodePoints) {
		t.Fatalf("summary was not trimmed or preserved: length=%d", len(got))
	}

	if _, err := checkCasenoteSummary(strings.Repeat("界", maxCasenoteSummaryCodePoints+1)); err == nil ||
		!strings.Contains(err.Error(), "1400 Unicode code points") {
		t.Fatalf("expected clear over-limit error, got %v", err)
	}

	input := createCasenoteInput{Title: "Local test", Summary: "  concise gist  ",
		Author: "research-agent (deepseek/deepseek-flash)"}
	if err := validateCreate(&input); err != nil {
		t.Fatalf("valid optional summary rejected: %v", err)
	}
	if input.Summary != "concise gist" {
		t.Fatalf("create summary = %q, want trimmed summary", input.Summary)
	}
}

func TestCasenoteSummaryUpdateOmissionPreservesAndEmptyClears(t *testing.T) {
	current := "Existing short summary"
	got, err := updatedCasenoteSummary(current, nil)
	if err != nil || got != current {
		t.Fatalf("omitted summary = %q, %v; want preserved %q", got, err, current)
	}
	empty := ""
	got, err = updatedCasenoteSummary(current, &empty)
	if err != nil || got != "" {
		t.Fatalf("empty summary = %q, %v; want clear", got, err)
	}
}

func TestUpdateCasenoteInputDistinguishesOmittedAndExplicitEmptySummary(t *testing.T) {
	empty := ""
	for _, test := range []struct {
		json      string
		wantValue *string
	}{
		{json: `{"id":1,"version":1,"actor":"agent (provider/model)"}`},
		{json: `{"id":1,"version":1,"actor":"agent (provider/model)","summary":""}`, wantValue: &empty},
	} {
		var input updateCasenoteInput
		request := httptest.NewRequest(http.MethodPost, "/api/casenotes/update", strings.NewReader(test.json))
		if err := decodeJSON(request, &input); err != nil {
			t.Fatal(err)
		}
		if test.wantValue == nil && input.Summary != nil {
			t.Fatalf("omitted summary decoded as %#v", input.Summary)
		}
		if test.wantValue != nil && (input.Summary == nil || *input.Summary != *test.wantValue) {
			t.Fatalf("explicit empty summary decoded as %#v", input.Summary)
		}
	}
}

func TestHumanReviewScoreAndPriorityTags(t *testing.T) {
	for _, raw := range []string{`{"human_review_score":-1}`, `{"human_review_score":6}`, `{"human_review_score":2.5}`, `{"human_review_score":"5"}`} {
		if _, err := normalizeFields(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid score %s", raw)
		}
	}
	for _, tag := range []string{"agent refusal", "working bug report", "near-miss exploit chain"} {
		input := createCasenoteInput{Title: "Local review", Author: "archivist (deepseek/deepseek-flash)", Tags: []string{tag}, Fields: json.RawMessage(`{"human_review_score":1}`)}
		if err := validateCreate(&input); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(input.Fields), `"human_review_score":5`) {
			t.Fatalf("tag %q did not promote score: %s", tag, input.Fields)
		}
	}
}

func TestLinkRequiresWeight(t *testing.T) {
	a := &app{}
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/links/create",
		strings.NewReader(`{"from_casenote_id":1,"to_casenote_id":2,"relation":"supports","author":"research-agent (deepseek/deepseek-flash)"}`))
	request.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	a.api(result, request)
	if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), "weight is required") {
		t.Fatalf("status %d: %s", result.Code, result.Body.String())
	}
}

func TestValidateCreateRequiresAttribution(t *testing.T) {
	input := createCasenoteInput{Title: "local experiment", Author: " "}
	if err := validateCreate(&input); err == nil || !strings.Contains(err.Error(), "actor") {
		t.Fatalf("expected author validation error, got %v", err)
	}
	input.Author = "anonymous"
	if err := validateCreate(&input); err == nil {
		t.Fatal("expected actor model identity to be required")
	}
	input.Author = "research-agent (deepseek/deepseek-flash)"
	if err := validateCreate(&input); err != nil {
		t.Fatalf("valid agent identity rejected: %v", err)
	}
}

func TestSearchBuildsTextAndTagFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/casefile-casenotes/_search" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Query struct {
				Bool struct {
					Must   []map[string]any `json:"must"`
					Filter []map[string]any `json:"filter"`
				} `json:"bool"`
			} `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Query.Bool.Must) != 1 || len(body.Query.Bool.Filter) != 4 {
			t.Errorf("unexpected search clauses: %#v", body.Query.Bool)
		}
		if got := body.Query.Bool.Filter[0]["term"].(map[string]any)["project_id"]; got != float64(42) {
			t.Errorf("project filter = %v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":{"total":{"value":2},"hits":[{"_id":"7"},{"_id":"3"}]}}`))
	}))
	defer server.Close()
	a := &app{search: newSearchClient(server.URL)}
	ids, total, err := a.searchCasenotesInElastic(context.Background(), searchInput{
		projectSelector: projectSelector{ProjectID: 42},
		Query:           "package owner", Tags: []string{"authz", "negative finding"}, Kind: "observation", Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || !reflect.DeepEqual(ids, []int64{7, 3}) {
		t.Fatalf("got ids=%v total=%d", ids, total)
	}
}

func TestSearchReviewScoreFilters(t *testing.T) {
	for _, test := range []struct {
		operator string
		want     map[string]any
	}{
		{"eq", map[string]any{"term": map[string]any{"human_review_score": float64(3)}}},
		{"gte", map[string]any{"range": map[string]any{"human_review_score": map[string]any{"gte": float64(3)}}}},
	} {
		t.Run(test.operator, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Query struct {
						Bool struct {
							Filter []map[string]any `json:"filter"`
						} `json:"bool"`
					} `json:"query"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body.Query.Bool.Filter) != 2 || !reflect.DeepEqual(body.Query.Bool.Filter[1], test.want) {
					t.Errorf("score filter = %#v, want %#v", body.Query.Bool.Filter, test.want)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"hits":{"total":{"value":0},"hits":[]}}`))
			}))
			defer server.Close()
			score := 3
			input := searchInput{projectSelector: projectSelector{ProjectID: 42}, ReviewScore: &score, ReviewOperator: test.operator, Limit: 20}
			if _, _, err := (&app{search: newSearchClient(server.URL)}).searchCasenotesInElastic(context.Background(), input); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSearchReviewFilterValidationAndIndexedScore(t *testing.T) {
	for _, test := range []struct {
		json string
		ok   bool
	}{
		{`{}`, true},
		{`{"review_score":1,"review_operator":"eq"}`, true},
		{`{"review_score":5,"review_operator":"gte"}`, true},
		{`{"review_score":0,"review_operator":"eq"}`, false},
		{`{"review_score":6,"review_operator":"eq"}`, false},
		{`{"review_score":3,"review_operator":"gt"}`, false},
		{`{"review_operator":"eq"}`, false},
	} {
		var input searchInput
		if err := json.Unmarshal([]byte(test.json), &input); err != nil {
			t.Fatal(err)
		}
		if got := input.validateReviewFilter() == nil; got != test.ok {
			t.Errorf("%s validity = %v, want %v", test.json, got, test.ok)
		}
	}
	for _, test := range []struct {
		fields string
		want   int
	}{
		{`{"human_review_score":4}`, 4},
		{`{}`, 0},
		{`{"human_review_score":"5"}`, 0},
		{`{"human_review_score":7}`, 0},
	} {
		if got := reviewScoreFromFields(json.RawMessage(test.fields)); got != test.want {
			t.Errorf("indexed score from %s = %d, want %d", test.fields, got, test.want)
		}
	}
}

func TestAPIAcceptsOnlySameOriginJSONPost(t *testing.T) {
	a := &app{}
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/casenotes/get", nil)
	result := httptest.NewRecorder()
	a.api(result, request)
	if result.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status %d", result.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "http://localhost/api/casenotes/get", strings.NewReader(`{"id":1}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://elsewhere.example")
	result = httptest.NewRecorder()
	a.api(result, request)
	if result.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status %d", result.Code)
	}
}

func TestSearchHealthRequiresAssignedPrimary(t *testing.T) {
	for _, test := range []struct {
		name        string
		body        string
		wantHealthy bool
	}{
		{"green", `{"status":"green","timed_out":false,"active_primary_shards":1}`, true},
		{"red", `{"status":"red","timed_out":true,"active_primary_shards":0}`, false},
		{"missing primary", `{"status":"yellow","timed_out":false,"active_primary_shards":0}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "HEAD /casefile-casenotes":
					w.WriteHeader(200)
				case "GET /_cluster/health/casefile-casenotes":
					_, _ = w.Write([]byte(test.body))
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
			}))
			defer server.Close()
			err := newSearchClient(server.URL).ping(context.Background())
			if (err == nil) != test.wantHealthy {
				t.Fatalf("health error = %v", err)
			}
		})
	}
}

func TestExistingSearchIndexGetsZeroReplicas(t *testing.T) {
	settingsWrites := 0
	mappingWrites := 0
	missingScoreCounts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "HEAD /casefile-casenotes":
			w.WriteHeader(200)
		case "PUT /casefile-casenotes/_settings":
			settingsWrites++
			var body struct {
				Index struct {
					Replicas int `json:"number_of_replicas"`
				} `json:"index"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Index.Replicas != 0 {
				t.Errorf("replicas = %d", body.Index.Replicas)
			}
			_, _ = w.Write([]byte(`{"acknowledged":true}`))
		case "PUT /casefile-casenotes/_mapping":
			mappingWrites++
			var body struct {
				Properties map[string]struct {
					Type string `json:"type"`
				} `json:"properties"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Properties["project_id"].Type != "long" || body.Properties["project"].Type != "keyword" || body.Properties["human_review_score"].Type != "byte" {
				t.Errorf("project mapping = %#v", body.Properties)
			}
			_, _ = w.Write([]byte(`{"acknowledged":true}`))
		case "POST /casefile-casenotes/_count":
			missingScoreCounts++
			var body struct {
				Query struct {
					Bool struct {
						MustNot map[string]struct {
							Field string `json:"field"`
						} `json:"must_not"`
					} `json:"bool"`
				} `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Query.Bool.MustNot["exists"].Field != "human_review_score" {
				t.Errorf("missing score query = %#v", body)
			}
			if missingScoreCounts == 1 {
				_, _ = w.Write([]byte(`{"count":12}`))
			} else {
				_, _ = w.Write([]byte(`{"count":0}`))
			}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	search := newSearchClient(server.URL)
	for i := 0; i < 2; i++ {
		reindex, err := search.ensureIndex(context.Background())
		if err != nil || reindex != (i == 0) {
			t.Fatalf("reindex=%v err=%v", reindex, err)
		}
	}
	restarted := newSearchClient(server.URL)
	if reindex, err := restarted.ensureIndex(context.Background()); err != nil || reindex {
		t.Fatalf("restarted index reindex=%v err=%v", reindex, err)
	}
	if settingsWrites != 2 {
		t.Fatalf("settings writes = %d", settingsWrites)
	}
	if mappingWrites != 2 {
		t.Fatalf("mapping writes = %d", mappingWrites)
	}
	if missingScoreCounts != 2 {
		t.Fatalf("missing score count queries = %d", missingScoreCounts)
	}
}

func TestProjectSlugFromName(t *testing.T) {
	if got := slugFromName("  Other Target / 2026  "); got != "other-target-2026" {
		t.Fatalf("slug = %q", got)
	}
}

func TestProjectSelectorUnmarshalsIntoCasenoteInput(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/casenotes/create",
		strings.NewReader(`{"project_id":7,"title":"example","author":"agent"}`))
	var input createCasenoteInput
	if err := decodeJSON(request, &input); err != nil {
		t.Fatal(err)
	}
	if input.ProjectID != 7 || input.Title != "example" {
		t.Fatalf("decoded input = %#v", input)
	}
}
