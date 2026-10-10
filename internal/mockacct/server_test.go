package mockacct

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		roll float64
		want string
	}{
		{0, "error"},
		{0.19, "error"},
		{0.20, "hang"},
		{0.29, "hang"},
		{0.30, "ok"},
		{0.99, "ok"},
	}
	for _, tc := range cases {
		if got := classify(tc.roll, 0.20, 0.10); got != tc.want {
			t.Fatalf("classify(%v) = %s, want %s", tc.roll, got, tc.want)
		}
	}
}

func TestDuplicateReturnsOriginalRef(t *testing.T) {
	srv := New()
	srv.SetRand(func() float64 { return 0.25 })
	srv.SetHang(30 * time.Millisecond)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	first := postRef(t, ts.URL, "client-1")
	if first.Code != http.StatusOK || first.ExternalRef == "" {
		t.Fatalf("first = %+v", first)
	}
	second := postRef(t, ts.URL, "client-1")
	if second.Code != http.StatusConflict || second.ExternalRef != first.ExternalRef {
		t.Fatalf("duplicate = %+v, want 409 %s", second, first.ExternalRef)
	}
}

func TestErrorIsNotRemembered(t *testing.T) {
	srv := New()
	roll := 0.0
	srv.SetRand(func() float64 { return roll })
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	failed := postRef(t, ts.URL, "client-2")
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", failed.Code)
	}
	roll = 0.80
	ok := postRef(t, ts.URL, "client-2")
	if ok.Code != http.StatusOK || ok.ExternalRef == "" {
		t.Fatalf("retry after 500 = %+v", ok)
	}
}

func TestConcurrentFirstPostAgreesOnOneRef(t *testing.T) {
	srv := New()
	srv.SetRand(func() float64 { return 0.80 })
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var wg sync.WaitGroup
	got := make([]response, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			got[i] = postRef(t, ts.URL, "client-race")
		}(i)
	}
	wg.Wait()

	var ref string
	oks, conflicts := 0, 0
	for _, item := range got {
		switch item.Code {
		case http.StatusOK:
			oks++
			ref = item.ExternalRef
		case http.StatusConflict:
			conflicts++
			if ref == "" {
				ref = item.ExternalRef
			} else if item.ExternalRef != ref {
				t.Fatalf("refs differ: %s and %s", ref, item.ExternalRef)
			}
		default:
			t.Fatalf("status %d", item.Code)
		}
	}
	if oks != 1 || conflicts != 1 || ref == "" {
		t.Fatalf("ok=%d conflict=%d ref=%s", oks, conflicts, ref)
	}
}

type response struct {
	Code        int
	ExternalRef string
	Error       string
}

func postRef(t *testing.T, base, clientRef string) response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"client_ref": clientRef, "amount_paise": 10000})
	res, err := http.Post(base+"/api/vouchers", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var payload struct {
		ExternalRef string `json:"external_ref"`
		Error       string `json:"error"`
	}
	_ = json.NewDecoder(res.Body).Decode(&payload)
	return response{Code: res.StatusCode, ExternalRef: payload.ExternalRef, Error: payload.Error}
}
