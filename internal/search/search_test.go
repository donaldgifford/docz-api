package search

import (
	"encoding/json"
	"testing"

	"github.com/meilisearch/meilisearch-go"
)

func TestFormatUnix(t *testing.T) {
	tests := []struct {
		name string
		sec  int64
		want string
	}{
		// Zero is the wire's not-applicable convention, not the epoch: a
		// record with no stamp serves "" like every other unset string field.
		{"zero renders empty", 0, ""},
		{"a stamp renders RFC3339 in UTC", 1750615451, "2025-06-22T18:04:11Z"},
		{"one second past the epoch is still a stamp", 1, "1970-01-01T00:00:01Z"},
		{"a negative stamp still renders", -1, "1969-12-31T23:59:59Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatUnix(tt.sec); got != tt.want {
				t.Errorf("formatUnix(%d) = %q, want %q", tt.sec, got, tt.want)
			}
		})
	}
}

// hitsFromJSON builds a meilisearch.Hits from a raw response fragment, so the
// decode path is exercised through the same JSON tags a live Meilisearch
// response carries.
func hitsFromJSON(t *testing.T, raw string) meilisearch.Hits {
	t.Helper()
	var hits meilisearch.Hits
	if err := json.Unmarshal([]byte(raw), &hits); err != nil {
		t.Fatalf("build hits from %q: %v", raw, err)
	}
	return hits
}

// TestDecodeHitsDates pins the two dated fields end to end: created passes
// through as the stored YYYY-MM-DD string, and updated_at converts from the
// index's Unix seconds to an RFC3339 stamp in UTC. A page hit carries a real
// stamp and an empty created — the shape toIndexPage produces.
func TestDecodeHitsDates(t *testing.T) {
	hits := hitsFromJSON(t, `[
	  {"source":"doc","repo":"acme/platform","doc_id":"RFC-0001","type":"rfc",
	   "title":"Structured logging","path":"docs/rfc/0001-structured-logging.md",
	   "status":"Accepted","author":"Jane Dev","created":"2026-01-15",
	   "updated_at":1750615451,"body":"full body",
	   "_formatted":{"body":"…structured <em>logging</em>…"}},
	  {"source":"page","repo":"acme/platform","title":"Setup Guide",
	   "path":"guides/setup.md","created":"","updated_at":1750615456,
	   "body":"walkthrough","_formatted":{"body":"a <em>walkthrough</em>"}}
	]`)

	got, err := decodeHits(hits)
	if err != nil {
		t.Fatalf("decodeHits: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("decoded %d hits, want 2", len(got))
	}

	doc := got[0]
	if doc.Created != "2026-01-15" {
		t.Errorf("doc created = %q, want 2026-01-15 (passed through, not converted)", doc.Created)
	}
	if doc.UpdatedAt != "2025-06-22T18:04:11Z" {
		t.Errorf("doc updated_at = %q, want 2025-06-22T18:04:11Z", doc.UpdatedAt)
	}
	if doc.Snippet != "…structured <em>logging</em>…" {
		t.Errorf("doc snippet = %q, want the _formatted body", doc.Snippet)
	}

	page := got[1]
	if page.Created != "" {
		t.Errorf("page created = %q, want empty (pages carry no authored date)", page.Created)
	}
	if page.UpdatedAt != "2025-06-22T18:04:16Z" {
		t.Errorf("page updated_at = %q, want a real stamp — repo_pages stamps every row", page.UpdatedAt)
	}
}

// TestDecodeHitsMissingDates covers the attribute-absent case: a hit that
// carries neither date decodes to the "" convention rather than an epoch
// stamp. This is the shape a stale retrieve list would produce, so the
// assertion also documents why the retrieve list is the first drop site.
func TestDecodeHitsMissingDates(t *testing.T) {
	hits := hitsFromJSON(t, `[{"source":"doc","repo":"acme/platform","doc_id":"RFC-0001",
	  "title":"No dates","_formatted":{"body":"body"}}]`)

	got, err := decodeHits(hits)
	if err != nil {
		t.Fatalf("decodeHits: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("decoded %d hits, want 1", len(got))
	}
	if got[0].Created != "" || got[0].UpdatedAt != "" {
		t.Errorf("created/updated_at = %q/%q, want both empty",
			got[0].Created, got[0].UpdatedAt)
	}
}

// TestSearchRetrievesDatedAttributes guards the retrieve list itself. A hit
// field can only arrive if Search asks Meilisearch for its attribute, and
// that list is invisible to every test that fakes the searcher — the drop
// site INV-0009 F2 found. Keep this in sync with SearchHit.
func TestSearchRetrievesDatedAttributes(t *testing.T) {
	want := []string{
		"source", "repo", "doc_id", "type", "title", "path",
		"status", "author", "created", "updated_at", "body",
	}
	got := retrieveAttributes
	if len(got) != len(want) {
		t.Fatalf("retrieve list = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("retrieve list[%d] = %q, want %q (full list %v)", i, got[i], want[i], got)
		}
	}
}
