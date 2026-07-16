package prom

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakePrometheus(t *testing.T, samples ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			http.NotFound(w, r)
			return
		}
		result := ""
		for i, s := range samples {
			if i > 0 {
				result += ","
			}
			result += fmt.Sprintf(`{"metric":{},"value":[0,"%s"]}`, s)
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[%s]}}`, result)
	}))
}

func TestQuerySingleSample(t *testing.T) {
	srv := fakePrometheus(t, "0.42")
	defer srv.Close()

	v, err := NewClient(srv.URL).Query(context.Background(), "up")
	if err != nil {
		t.Fatal(err)
	}
	if v != 0.42 {
		t.Fatalf("got %v, want 0.42", v)
	}
}

func TestQueryEmptyVectorIsErrNoData(t *testing.T) {
	srv := fakePrometheus(t)
	defer srv.Close()

	_, err := NewClient(srv.URL).Query(context.Background(), "up")
	if !errors.Is(err, ErrNoData) {
		t.Fatalf("got %v, want ErrNoData", err)
	}
}

func TestQueryMultiSampleIsError(t *testing.T) {
	srv := fakePrometheus(t, "1", "2")
	defer srv.Close()

	_, err := NewClient(srv.URL).Query(context.Background(), "up")
	if err == nil || errors.Is(err, ErrNoData) {
		t.Fatalf("got %v, want aggregation error", err)
	}
}
