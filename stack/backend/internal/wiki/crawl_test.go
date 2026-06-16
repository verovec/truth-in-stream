package wiki

import (
	"net/http"
	"testing"
)

// categoryClient builds an APIClient whose handler serves categorymembers bodies
// keyed by cmtitle, defaulting to an empty member list for an unknown category.
func categoryClient(t *testing.T, byCat map[string]string) *APIClient {
	t.Helper()
	return newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("list") != "categorymembers" {
			t.Errorf("list = %q, want categorymembers", q.Get("list"))
		}
		body, ok := byCat[q.Get("cmtitle")]
		if !ok {
			body = `{"query":{"categorymembers":[]}}`
		}
		_, _ = w.Write([]byte(body))
	})
}

func TestCategoryMembersBFSDepthAndDedup(t *testing.T) {
	t.Parallel()
	c := categoryClient(t, map[string]string{
		"Category:Root": `{"query":{"categorymembers":[
			{"pageid":1,"ns":0,"title":"A","type":"page"},
			{"pageid":2,"ns":0,"title":"B","type":"page"},
			{"pageid":0,"ns":14,"title":"Category:Sub","type":"subcat"}]}}`,
		"Category:Sub": `{"query":{"categorymembers":[
			{"pageid":2,"ns":0,"title":"B","type":"page"},
			{"pageid":3,"ns":0,"title":"C","type":"page"}]}}`,
	})

	got, err := c.CategoryMembers(t.Context(), []string{"Category:Root"}, 1, 100)
	if err != nil {
		t.Fatalf("CategoryMembers: %v", err)
	}
	// A, B, C deduped (B appears in both); the subcat itself is not a page.
	if len(got) != 3 {
		t.Fatalf("got %d members, want 3: %+v", len(got), got)
	}
	ids := map[int64]bool{}
	for _, m := range got {
		ids[m.PageID] = true
	}
	if !ids[1] || !ids[2] || !ids[3] {
		t.Errorf("missing expected page ids, got %v", ids)
	}
}

func TestCategoryMembersDepthZeroSkipsSubcats(t *testing.T) {
	t.Parallel()
	c := categoryClient(t, map[string]string{
		"Category:Root": `{"query":{"categorymembers":[
			{"pageid":1,"ns":0,"title":"A","type":"page"},
			{"pageid":0,"ns":14,"title":"Category:Sub","type":"subcat"}]}}`,
		"Category:Sub": `{"query":{"categorymembers":[{"pageid":3,"ns":0,"title":"C","type":"page"}]}}`,
	})

	got, err := c.CategoryMembers(t.Context(), []string{"Category:Root"}, 0, 100)
	if err != nil {
		t.Fatalf("CategoryMembers: %v", err)
	}
	if len(got) != 1 || got[0].PageID != 1 {
		t.Fatalf("got %+v, want only page 1", got)
	}
}

func TestCategoryMembersRespectsMaxPages(t *testing.T) {
	t.Parallel()
	c := categoryClient(t, map[string]string{
		"Category:Root": `{"query":{"categorymembers":[
			{"pageid":1,"ns":0,"title":"A","type":"page"},
			{"pageid":2,"ns":0,"title":"B","type":"page"},
			{"pageid":3,"ns":0,"title":"C","type":"page"}]}}`,
	})

	got, err := c.CategoryMembers(t.Context(), []string{"Category:Root"}, 0, 2)
	if err != nil {
		t.Fatalf("CategoryMembers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want capped at 2", len(got))
	}
}

func TestCategoryMembersFollowsContinuation(t *testing.T) {
	t.Parallel()
	// First request has no cmcontinue and returns page 1 plus a continue token;
	// the second carries the token and returns page 2 with no further token.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cmcontinue") == "" {
			_, _ = w.Write([]byte(`{"continue":{"cmcontinue":"next","continue":"-||"},"query":{"categorymembers":[{"pageid":1,"ns":0,"title":"A","type":"page"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"query":{"categorymembers":[{"pageid":2,"ns":0,"title":"B","type":"page"}]}}`))
	})

	got, err := c.CategoryMembers(t.Context(), []string{"Category:Root"}, 0, 100)
	if err != nil {
		t.Fatalf("CategoryMembers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d members, want 2 across the continuation: %+v", len(got), got)
	}
}

func TestCategoryMembersSurfacesAPIError(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"code":"badcategory","info":"no such category"}}`))
	})
	if _, err := c.CategoryMembers(t.Context(), []string{"Category:Nope"}, 0, 100); err == nil {
		t.Fatal("CategoryMembers with API error = nil, want error")
	}
}
