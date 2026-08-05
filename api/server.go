package api

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/messaging"
)

// Server provides an HTTP API for sending messages.
type Server struct {
	clients []*ilink.Client
	addr    string
	ready   chan struct{}
	once    sync.Once
}

// NewServer creates an API server.
func NewServer(clients []*ilink.Client, addr string) *Server {
	if addr == "" {
		addr = "127.0.0.1:18011"
	}
	return &Server{clients: clients, addr: addr, ready: make(chan struct{})}
}

// Ready 在监听端口真正绑定成功后关闭，供调度器避免启动竞态。
func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

// SendRequest is the JSON body for POST /api/send.
type SendRequest struct {
	To       string `json:"to"`
	Text     string `json:"text,omitempty"`
	MediaURL string `json:"media_url,omitempty"` // image/video/file URL
}

// Run starts the HTTP server. Blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: s.addr, Handler: s.handler()}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	s.once.Do(func() { close(s.ready) })
	log.Printf("[api] listening on %s", listener.Addr())
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/send", s.handleSend)
	return mux
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.To == "" {
		http.Error(w, `"to" is required`, http.StatusBadRequest)
		return
	}
	if req.Text == "" && req.MediaURL == "" {
		http.Error(w, `"text" or "media_url" is required`, http.StatusBadRequest)
		return
	}

	if len(s.clients) == 0 {
		http.Error(w, "no accounts configured", http.StatusServiceUnavailable)
		return
	}

	// Use the first client
	client := s.clients[0]
	ctx := r.Context()

	// Send text if provided
	if req.Text != "" {
		if err := messaging.SendTextReply(ctx, client, req.To, req.Text, "", ""); err != nil {
			log.Printf("[api] send text failed: %v", err)
			http.Error(w, "send text failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[api] sent text to %s", ilink.LogLabel(req.To))

		// Extract and send any markdown images embedded in text
		for _, imgURL := range messaging.ExtractImageURLs(req.Text) {
			if err := messaging.SendMediaFromURL(ctx, client, req.To, imgURL, ""); err != nil {
				log.Printf("[api] send extracted image failed: %v", err)
			} else {
				log.Printf("[api] sent extracted image to %s", ilink.LogLabel(req.To))
			}
		}
	}

	// Send media if provided
	if req.MediaURL != "" {
		if err := messaging.SendMediaFromURL(ctx, client, req.To, req.MediaURL, ""); err != nil {
			log.Printf("[api] send media failed: %v", err)
			http.Error(w, "send media failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[api] sent media to %s", ilink.LogLabel(req.To))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
