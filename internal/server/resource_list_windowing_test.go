package server

// Tests for server-side windowing on /api/resources/{kind} (issue #1303).
// The informer cache holds the full list in memory; sort+window server-side
// is cheap and is what lets the frontend page through 25k+ objects instead
// of refusing to render them.
//
// Contract:
//   - Any of sort/order/offset/limit/q switches the response from the legacy
//     bare array to a {total, items} envelope.
//   - total counts the post-filter (q), pre-window set.
//   - Sort is stable with a name tiebreaker, so consecutive pages share no
//     items and miss none — pinned by fetching two adjacent pages.
//   - Multi-namespace requests sort AFTER merging (a per-namespace sort
//     concatenation would interleave wrongly).
//   - Windowing composes with include=summary (summary projects the page).

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"testing"
)

type listWindowEnvelope struct {
	Total int               `json:"total"`
	Items []map[string]any  `json:"items"`
}

func fetchWindow(t *testing.T, path string) listWindowEnvelope {
	t.Helper()
	resp := get(t, path)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d: %s", path, resp.StatusCode, body)
	}
	var env listWindowEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("GET %s: expected {total, items} envelope, got: %.200s (%v)", path, body, err)
	}
	return env
}

func itemNames(items []map[string]any) []string {
	names := make([]string, 0, len(items))
	for _, it := range items {
		if n, ok := nested(it, "metadata", "name"); ok {
			names = append(names, n.(string))
		}
	}
	return names
}

func TestPodListWindowing(t *testing.T) {
	const n = 60
	cleanup := seedSummaryFixturePods(t, n)
	defer cleanup()

	base := "/api/resources/pods?namespaces=" + summaryFixtureNS

	t.Run("no window params keeps the legacy bare array", func(t *testing.T) {
		body, items := fetchRawList(t, base)
		if len(items) != n {
			t.Fatalf("bare array: expected %d items, got %d (body starts %.60s)", n, len(items), body)
		}
	})

	t.Run("limit+offset return a stable envelope with adjacent pages", func(t *testing.T) {
		page1 := fetchWindow(t, base+"&sort=name&order=asc&offset=0&limit=25")
		if page1.Total != n {
			t.Errorf("total: want %d, got %d", n, page1.Total)
		}
		if len(page1.Items) != 25 {
			t.Fatalf("page1: want 25 items, got %d", len(page1.Items))
		}
		names1 := itemNames(page1.Items)
		if !sort.StringsAreSorted(names1) {
			t.Errorf("page1 not name-sorted: %v", names1)
		}

		page2 := fetchWindow(t, base+"&sort=name&order=asc&offset=25&limit=25")
		names2 := itemNames(page2.Items)
		if len(names2) != 25 {
			t.Fatalf("page2: want 25 items, got %d", len(names2))
		}
		// Fixture pods are heavy-pod-000..059: page boundaries are exact.
		if names1[0] != "heavy-pod-000" || names1[24] != "heavy-pod-024" || names2[0] != "heavy-pod-025" {
			t.Errorf("page boundary broken: page1 %s..%s, page2 starts %s", names1[0], names1[24], names2[0])
		}

		tail := fetchWindow(t, base+"&sort=name&order=asc&offset=50&limit=25")
		if len(tail.Items) != 10 {
			t.Errorf("tail page past end: want 10 items, got %d", len(tail.Items))
		}
	})

	t.Run("desc order reverses", func(t *testing.T) {
		env := fetchWindow(t, base+"&sort=name&order=desc&limit=3")
		names := itemNames(env.Items)
		want := []string{"heavy-pod-059", "heavy-pod-058", "heavy-pod-057"}
		for i, w := range want {
			if names[i] != w {
				t.Errorf("desc[%d]: want %s, got %s", i, w, names[i])
			}
		}
	})

	t.Run("q filters on name substring and total reflects the filtered set", func(t *testing.T) {
		env := fetchWindow(t, base+"&q=heavy-pod-00&limit=100")
		if env.Total != 10 || len(env.Items) != 10 {
			t.Errorf("q filter: want total=10 items=10, got total=%d items=%d", env.Total, len(env.Items))
		}
	})

	t.Run("windowing composes with include=summary", func(t *testing.T) {
		env := fetchWindow(t, base+"&include=summary&sort=name&limit=5")
		if env.Total != n || len(env.Items) != 5 {
			t.Fatalf("summary window: want total=%d items=5, got total=%d items=%d", n, env.Total, len(env.Items))
		}
		if _, ok := nested(env.Items[0], "spec", "volumes"); ok {
			t.Error("summary window items still carry spec.volumes")
		}
		if _, ok := nested(env.Items[0], "status", "phase"); !ok {
			t.Error("summary window items missing status.phase")
		}
	})

	t.Run("multi-namespace merge sorts after merging", func(t *testing.T) {
		// TestMain seeds pods outside the fixture namespace; the merged list
		// must be globally sorted, not per-namespace concatenated.
		all := fetchWindow(t, "/api/resources/pods?sort=name&order=asc&limit=1000")
		names := itemNames(all.Items)
		if !sort.StringsAreSorted(names) {
			shown := names
			if len(shown) > 10 {
				shown = shown[:10]
			}
			t.Errorf("all-namespace window not globally sorted: %v", shown)
		}
		if all.Total < n {
			t.Errorf("all-namespace total %d < fixture count %d", all.Total, n)
		}
	})

	t.Run("invalid window params are 400", func(t *testing.T) {
		for _, q := range []string{"sort=bogus", "order=sideways", "offset=-1", "limit=-5", "limit=nan"} {
			resp := get(t, base+"&"+q)
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s: want 400, got %d", q, resp.StatusCode)
			}
		}
	})
}

