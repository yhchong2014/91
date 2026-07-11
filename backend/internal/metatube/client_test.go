package metatube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtractCode(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"ABP-123.mp4", "ABP-123"},
		{"[ABP-123] some title.mp4", "ABP-123"},
		{"ssis-456.mkv", "SSIS-456"},
		{"FC2-PPV-1234567.mp4", "FC2-PPV-1234567"},
		{"STARS-901 4K.mp4", "STARS-901"},
		{"123456-789.mp4", "123456-789"},
		{"random-home-video.mp4", ""},
		{"my family trip.mp4", ""},
		{"CAWD-001 title here.mp4", "CAWD-001"},
		{"[夸克] MIDE-987 full.mp4", "MIDE-987"},
	}
	for _, tc := range tests {
		got := ExtractCode(tc.filename)
		if got != tc.want {
			t.Errorf("ExtractCode(%q) = %q, want %q", tc.filename, got, tc.want)
		}
	}
}

func TestSearchByCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/movies/search" {
			q := r.URL.Query().Get("q")
			if q != "ABP-123" {
				t.Errorf("search query = %q, want ABP-123", q)
			}
			resp := map[string]interface{}{
				"data": []map[string]interface{}{
					{"provider": "javbus", "id": "ABP-123", "number": "ABP-123", "title": "Test Title"},
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	c := New(Config{ServerURL: srv.URL})
	results, err := c.SearchByCode(context.Background(), "ABP-123")
	if err != nil {
		t.Fatalf("SearchByCode: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Provider != "javbus" {
		t.Errorf("provider = %q, want javbus", results[0].Provider)
	}
	if results[0].Code != "ABP-123" {
		t.Errorf("code = %q, want ABP-123", results[0].Code)
	}
}

func TestSearchByCodeEmpty(t *testing.T) {
	c := New(Config{ServerURL: "http://unused"})
	results, err := c.SearchByCode(context.Background(), "")
	if err != nil {
		t.Fatalf("SearchByCode empty: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil for empty code, got %v", results)
	}
}

func TestGetMovieDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/movies/javbus/ABP-123" {
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"provider":     "javbus",
					"id":           "ABP-123",
					"number":       "ABP-123",
					"title":        "Full Title Here",
					"actors":       []map[string]string{{"name": "Actress A"}},
					"genres":       []string{"Genre1", "Genre2"},
					"release_date": "2024-01-15",
					"runtime":      120,
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	c := New(Config{ServerURL: srv.URL})
	meta, err := c.GetMovieDetail(context.Background(), "javbus", "ABP-123")
	if err != nil {
		t.Fatalf("GetMovieDetail: %v", err)
	}
	if meta.Title != "Full Title Here" {
		t.Errorf("title = %q", meta.Title)
	}
	if len(meta.Actors) != 1 || meta.Actors[0].Name != "Actress A" {
		t.Errorf("actors = %+v", meta.Actors)
	}
	if len(meta.Genres) != 2 {
		t.Errorf("genres = %+v", meta.Genres)
	}
	if meta.Runtime != 120 {
		t.Errorf("runtime = %d, want 120", meta.Runtime)
	}
}

func TestGetPrimaryImageURL(t *testing.T) {
	c := New(Config{ServerURL: "http://mt.local:8080", Token: "mytoken"})
	got := c.GetPrimaryImageURL("fanza", "abc123")
	want := "http://mt.local:8080/api/v1/images/primary/fanza/abc123?token=mytoken"
	if got != want {
		t.Errorf("GetPrimaryImageURL = %q, want %q", got, want)
	}
}

func TestGetPrimaryImageURLNoToken(t *testing.T) {
	c := New(Config{ServerURL: "http://mt.local:8080"})
	got := c.GetPrimaryImageURL("fanza", "abc123")
	want := "http://mt.local:8080/api/v1/images/primary/fanza/abc123"
	if got != want {
		t.Errorf("GetPrimaryImageURL = %q, want %q", got, want)
	}
}

func TestScrapeByFilename(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/movies/search":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{"provider": "fanza", "id": "abp123", "number": "ABP-123", "title": "Search Hit"},
				},
			})
		case "/api/v1/movies/fanza/abp123":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"provider": "fanza",
					"id":       "abp123",
					"number":   "ABP-123",
					"title":    "Detailed Title",
					"actors":   []map[string]string{{"name": "Star"}},
					"genres":   []string{"Cat1"},
				},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := New(Config{ServerURL: srv.URL})
	meta, err := c.ScrapeByFilename(context.Background(), "ABP-123.mp4")
	if err != nil {
		t.Fatalf("ScrapeByFilename: %v", err)
	}
	if meta == nil {
		t.Fatal("expected metadata, got nil")
	}
	if meta.Title != "Detailed Title" {
		t.Errorf("title = %q", meta.Title)
	}
}

func TestScrapeByFilenameNoMatch(t *testing.T) {
	c := New(Config{ServerURL: "http://unused"})
	meta, err := c.ScrapeByFilename(context.Background(), "random-home-video.mp4")
	if err != nil {
		t.Fatalf("ScrapeByFilename: %v", err)
	}
	if meta != nil {
		t.Errorf("expected nil metadata for non-code filename, got %+v", meta)
	}
}

func TestBearerTokenSent(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}})
	}))
	defer srv.Close()

	c := New(Config{ServerURL: srv.URL, Token: "secret123"})
	_, _ = c.SearchByCode(context.Background(), "TEST-001")

	if gotAuth != "Bearer secret123" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer secret123")
	}
}

func TestHTTPErrorHandling(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("Internal Server Error"))
	}))
	defer srv.Close()

	c := New(Config{ServerURL: srv.URL})
	_, err := c.SearchByCode(context.Background(), "TEST-001")
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}
