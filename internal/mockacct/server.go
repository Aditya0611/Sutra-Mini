package mockacct

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	mathrand "math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// Server is the stand-in accounting API.
// A first-time client_ref fails with HTTP 500 for 20% of rolls, waits and then
// succeeds for the next 10%, and succeeds immediately otherwise.
// The external reference is stored before a wait, so a retry during that wait
// gets HTTP 409 and the original reference. A 500 is not stored.
type Server struct {
	mu       sync.Mutex
	posted   map[string]string
	randFn   func() float64
	hang     time.Duration
	failRate float64
	hangRate float64
}

func New() *Server {
	return &Server{
		posted:   map[string]string{},
		randFn:   mathrand.Float64,
		hang:     5 * time.Second,
		failRate: 0.20,
		hangRate: 0.10,
	}
}

func (s *Server) SetFailRate(rate float64) {
	s.mu.Lock()
	s.failRate = rate
	s.mu.Unlock()
}

func (s *Server) SetRand(fn func() float64) {
	s.mu.Lock()
	s.randFn = fn
	s.mu.Unlock()
}

func (s *Server) SetHang(d time.Duration) {
	s.mu.Lock()
	s.hang = d
	s.mu.Unlock()
}

// Remember records a client_ref that accounting already accepted.
func (s *Server) Remember(clientRef, externalRef string) {
	s.mu.Lock()
	s.posted[clientRef] = externalRef
	s.mu.Unlock()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/vouchers", s.post)
	return mux
}

func classify(roll, failRate, hangRate float64) string {
	// Compare in thousandths so 0.20 + 0.10 stays the same boundary as 0.30.
	r := int(roll*1000 + 1e-6)
	failEnd := int(failRate*1000 + 1e-6)
	hangEnd := int((failRate+hangRate)*1000 + 1e-6)
	if r < failEnd {
		return "error"
	}
	if r < hangEnd {
		return "hang"
	}
	return "ok"
}

func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientRef   string `json:"client_ref"`
		AmountPaise int64  `json:"amount_paise"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ClientRef == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "client_ref is required"})
		return
	}

	s.mu.Lock()
	if ref, ok := s.posted[body.ClientRef]; ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{
			"external_ref": ref,
			"error":        "duplicate client_ref",
		})
		return
	}
	outcome := classify(s.randFn(), s.failRate, s.hangRate)
	if outcome == "error" {
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "accounting unavailable"})
		return
	}
	ref, err := newRef()
	if err != nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not allocate external_ref"})
		return
	}
	s.posted[body.ClientRef] = ref
	hang := s.hang
	s.mu.Unlock()

	if outcome == "hang" {
		timer := time.NewTimer(hang)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"external_ref": ref})
}

func newRef() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "acct_" + hex.EncodeToString(buf[:]), nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
