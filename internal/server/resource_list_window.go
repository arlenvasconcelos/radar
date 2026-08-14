package server

import (
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Server-side windowing for /api/resources/{kind} (issue #1303). The informer
// cache holds the whole list in memory, so filter+sort+slice here is cheap —
// and it's what lets the frontend page through 25k+ objects instead of
// refusing to render them. Any of sort/order/offset/limit/q switches the
// response from the legacy bare array to a {total, items} envelope; total is
// the post-filter, pre-window count the pager needs.
//
// Windowing runs BEFORE the summary projection on purpose: every raw list
// item (typed pointer or unstructured) implements metav1.Object, which the
// sort keys need — and projecting only the returned page is strictly less
// work than projecting the full list.

type listWindow struct {
	sortKey string // name | namespace | creationTimestamp
	desc    bool
	offset  int
	limit   int // -1 = to the end
	q       string
	active  bool
}

func parseListWindow(r *http.Request) (listWindow, error) {
	vals := r.URL.Query()
	w := listWindow{sortKey: "name", limit: -1}

	if v := vals.Get("sort"); v != "" {
		switch v {
		case "name", "namespace", "creationTimestamp":
			w.sortKey = v
		default:
			return w, fmt.Errorf("unknown sort=%q (want: name, namespace, creationTimestamp)", v)
		}
		w.active = true
	}
	if v := vals.Get("order"); v != "" {
		switch v {
		case "asc":
		case "desc":
			w.desc = true
		default:
			return w, fmt.Errorf("unknown order=%q (want: asc, desc)", v)
		}
		w.active = true
	}
	if v := vals.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return w, fmt.Errorf("invalid offset=%q (want a non-negative integer)", v)
		}
		w.offset = n
		w.active = true
	}
	if v := vals.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return w, fmt.Errorf("invalid limit=%q (want a non-negative integer)", v)
		}
		w.limit = n
		w.active = true
	}
	if v := vals.Get("q"); v != "" {
		w.q = strings.ToLower(v)
		w.active = true
	}
	return w, nil
}

type windowEntry struct {
	name string
	ns   string
	ts   int64
	item any
}

// applyListWindow filters, sorts and slices a list result. Items that don't
// implement metav1.Object (nothing on the list paths today) sort with empty
// keys rather than being dropped — windowing must never lose items.
func applyListWindow(result any, w listWindow) (total int, page []any) {
	v := reflect.ValueOf(result)
	if result == nil || v.Kind() != reflect.Slice {
		return 0, []any{}
	}

	entries := make([]windowEntry, 0, v.Len())
	for i := 0; i < v.Len(); i++ {
		item := v.Index(i).Interface()
		e := windowEntry{item: item}
		if obj, ok := item.(metav1.Object); ok {
			e.name = obj.GetName()
			e.ns = obj.GetNamespace()
			e.ts = obj.GetCreationTimestamp().UnixNano()
		}
		if w.q != "" &&
			!strings.Contains(strings.ToLower(e.name), w.q) &&
			!strings.Contains(strings.ToLower(e.ns), w.q) {
			continue
		}
		entries = append(entries, e)
	}

	less := func(a, b windowEntry) bool {
		switch w.sortKey {
		case "namespace":
			if a.ns != b.ns {
				return a.ns < b.ns
			}
		case "creationTimestamp":
			if a.ts != b.ts {
				return a.ts < b.ts
			}
		}
		return a.name < b.name
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if w.desc {
			return less(entries[j], entries[i])
		}
		return less(entries[i], entries[j])
	})

	total = len(entries)
	start := w.offset
	if start > total {
		start = total
	}
	end := total
	if w.limit >= 0 && start+w.limit < end {
		end = start + w.limit
	}
	page = make([]any, 0, end-start)
	for _, e := range entries[start:end] {
		page = append(page, e.item)
	}
	return total, page
}

// writeListResult is the single exit for list responses: applies windowing
// (envelope) and/or the summary projections, preserving the legacy bare-array
// shape when no window params were sent.
func (s *Server) writeListResult(w http.ResponseWriter, result any, includeSummary bool, window listWindow) {
	if window.active {
		total, page := applyListWindow(result, window)
		var items any = page
		if includeSummary {
			items = applySummaryStrip(items)
			items = applyTypedSummary(items)
		}
		s.writeJSON(w, map[string]any{"total": total, "items": items})
		return
	}
	if includeSummary {
		result = applySummaryStrip(result)
		result = applyTypedSummary(result)
	}
	s.writeJSON(w, result)
}
