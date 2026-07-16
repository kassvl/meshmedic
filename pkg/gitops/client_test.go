package gitops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeGitHub records the API calls Open makes and serves canned responses.
type fakeGitHub struct {
	t          *testing.T
	calls      []string
	fileExists bool
	putBody    map[string]string
	pullBody   map[string]string
}

func (f *fakeGitHub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			f.t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		key := r.Method + " " + r.URL.Path
		f.calls = append(f.calls, key)
		switch {
		case key == "GET /repos/o/r":
			fmt.Fprint(w, `{"default_branch":"main"}`)
		case key == "GET /repos/o/r/git/ref/heads/main":
			fmt.Fprint(w, `{"object":{"sha":"abc123"}}`)
		case key == "POST /repos/o/r/git/refs":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/o/r/contents/"):
			if f.fileExists {
				fmt.Fprint(w, `{"sha":"blob456"}`)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/repos/o/r/contents/"):
			json.NewDecoder(r.Body).Decode(&f.putBody)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		case key == "POST /repos/o/r/pulls":
			json.NewDecoder(r.Body).Decode(&f.pullBody)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"html_url":"https://github.com/o/r/pull/1"}`)
		default:
			f.t.Errorf("unexpected call %s", key)
			w.WriteHeader(http.StatusTeapot)
		}
	})
}

func testPR() PullRequest {
	return PullRequest{
		Branch:        "meshmedic/test-1",
		Title:         "[meshmedic] test",
		Body:          "## Incident\nreport body",
		Path:          "istio/demo/payments.yaml",
		Content:       []byte("kind: VirtualService\n"),
		CommitMessage: "meshmedic: shift traffic",
	}
}

func TestOpenCreatesBranchCommitAndPR(t *testing.T) {
	f := &fakeGitHub{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	c, err := NewClient(srv.URL, "o/r", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	url, err := c.Open(context.Background(), testPR())
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/o/r/pull/1" {
		t.Fatalf("got url %q", url)
	}

	want := []string{
		"GET /repos/o/r",
		"GET /repos/o/r/git/ref/heads/main",
		"POST /repos/o/r/git/refs",
		"GET /repos/o/r/contents/istio/demo/payments.yaml",
		"PUT /repos/o/r/contents/istio/demo/payments.yaml",
		"POST /repos/o/r/pulls",
	}
	if len(f.calls) != len(want) {
		t.Fatalf("calls %v, want %v", f.calls, want)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, f.calls[i], want[i])
		}
	}

	decoded, err := base64.StdEncoding.DecodeString(f.putBody["content"])
	if err != nil || string(decoded) != "kind: VirtualService\n" {
		t.Fatalf("committed content %q (decode err %v)", decoded, err)
	}
	if _, hasSHA := f.putBody["sha"]; hasSHA {
		t.Fatal("sha sent for a new file")
	}
	if f.pullBody["body"] != "## Incident\nreport body" {
		t.Fatalf("PR body %q, report must pass through unchanged", f.pullBody["body"])
	}
	if f.pullBody["base"] != "main" {
		t.Fatalf("PR base %q, want resolved default branch", f.pullBody["base"])
	}
}

func TestOpenUpdatesExistingFileWithSHA(t *testing.T) {
	f := &fakeGitHub{t: t, fileExists: true}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	c, _ := NewClient(srv.URL, "o/r", "test-token")
	if _, err := c.Open(context.Background(), testPR()); err != nil {
		t.Fatal(err)
	}
	if f.putBody["sha"] != "blob456" {
		t.Fatalf("sha %q, want existing blob sha for an update", f.putBody["sha"])
	}
}

func TestNewClientRejectsBareRepoName(t *testing.T) {
	if _, err := NewClient("", "just-a-name", "t"); err == nil {
		t.Fatal("want error for repo without owner/")
	}
}

func TestPathFor(t *testing.T) {
	got, err := PathFor("istio/{{.namespace}}/{{.scenario}}.yaml",
		map[string]string{"namespace": "demo"}, "canary-latency-rollback")
	if err != nil {
		t.Fatal(err)
	}
	if got != "istio/demo/canary-latency-rollback.yaml" {
		t.Fatalf("got %q", got)
	}
	if _, err := PathFor("{{.missing}}", map[string]string{}, "x"); err == nil {
		t.Fatal("want error for missing path parameter")
	}
}

func TestBranchForIsUniquePerEpisode(t *testing.T) {
	a := BranchFor("x", time.Unix(100, 0))
	b := BranchFor("x", time.Unix(101, 0))
	if a == b {
		t.Fatal("branches for different episodes must differ")
	}
}
