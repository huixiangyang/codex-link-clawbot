package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	ScopeSendText  = "send:text"
	ScopeSendMedia = "send:media"
	maxSendBody    = 32 << 10
	maxTextRunes   = 8000
	maxMediaURL    = 2048
)

var (
	callerIDPattern       = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{15,127}$`)
)

type Delivery interface {
	HasOwner(owner string) bool
	SendText(context.Context, string, string, string) error
	SendMedia(context.Context, string, string) error
}

type AccessToken struct {
	CallerID    string
	TokenSHA256 string
	Scopes      []string
}

type ServerConfig struct {
	ListenAddr        string
	ProxyMode         bool
	TrustedProxyCIDRs []string
	Tokens            []AccessToken
}

type accessCredential struct {
	callerID string
	hash     []byte
	scopes   map[string]bool
}

// Server 只承载显式启用并鉴权的主动发送面；健康与管理路由不在 TCP 上注册。
type Server struct {
	delivery       Delivery
	receipts       *SendReceiptStore
	addr           string
	proxyMode      bool
	trustedProxies []*net.IPNet
	credentials    []accessCredential
	ready          chan struct{}
	once           sync.Once
}

func NewServer(delivery Delivery, config ServerConfig, receipts *SendReceiptStore) (*Server, error) {
	if delivery == nil || receipts == nil {
		return nil, fmt.Errorf("send API delivery and receipt store are required")
	}
	if strings.TrimSpace(config.ListenAddr) == "" {
		return nil, fmt.Errorf("send API listen address is required")
	}
	host, portText, err := net.SplitHostPort(config.ListenAddr)
	port, portErr := strconv.Atoi(portText)
	listenIP := net.ParseIP(host)
	if err != nil || portErr != nil || listenIP == nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("send API listen address must use an IP and explicit port")
	}
	if !config.ProxyMode && !listenIP.IsLoopback() {
		return nil, fmt.Errorf("send API plaintext listener must be loopback")
	}
	server := &Server{
		delivery:  delivery,
		receipts:  receipts,
		addr:      config.ListenAddr,
		proxyMode: config.ProxyMode,
		ready:     make(chan struct{}),
	}
	if len(config.TrustedProxyCIDRs) > 16 {
		return nil, fmt.Errorf("too many trusted proxy networks")
	}
	for _, raw := range config.TrustedProxyCIDRs {
		_, network, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("parse trusted proxy network: %w", err)
		}
		if network.String() != raw {
			return nil, fmt.Errorf("trusted proxy network must be canonical")
		}
		prefixLength, _ := network.Mask.Size()
		if prefixLength == 0 {
			return nil, fmt.Errorf("trusted proxy network cannot trust every source")
		}
		server.trustedProxies = append(server.trustedProxies, network)
	}
	if server.proxyMode && len(server.trustedProxies) == 0 {
		return nil, fmt.Errorf("proxy mode requires trusted proxy networks")
	}
	if !server.proxyMode && len(server.trustedProxies) != 0 {
		return nil, fmt.Errorf("trusted proxy networks require proxy mode")
	}
	if len(config.Tokens) == 0 || len(config.Tokens) > 16 {
		return nil, fmt.Errorf("send API requires access tokens")
	}
	seenCallers := make(map[string]bool, len(config.Tokens))
	seenHashes := make(map[string]bool, len(config.Tokens))
	for _, token := range config.Tokens {
		if !callerIDPattern.MatchString(token.CallerID) || seenCallers[token.CallerID] {
			return nil, fmt.Errorf("invalid send API caller")
		}
		seenCallers[token.CallerID] = true
		digest, err := hex.DecodeString(token.TokenSHA256)
		if err != nil || len(digest) != sha256.Size || strings.ToLower(token.TokenSHA256) != token.TokenSHA256 || seenHashes[token.TokenSHA256] {
			return nil, fmt.Errorf("invalid send API token hash")
		}
		seenHashes[token.TokenSHA256] = true
		scopes := make(map[string]bool, len(token.Scopes))
		for _, scope := range token.Scopes {
			if scope != ScopeSendText && scope != ScopeSendMedia || scopes[scope] {
				return nil, fmt.Errorf("invalid send API token scope")
			}
			scopes[scope] = true
		}
		if len(scopes) == 0 {
			return nil, fmt.Errorf("send API token scope is required")
		}
		server.credentials = append(server.credentials, accessCredential{callerID: token.CallerID, hash: digest, scopes: scopes})
	}
	return server, nil
}

func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              s.addr,
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	s.once.Do(func() { close(s.ready) })
	log.Printf("[send-api] listening on %s", listener.Addr())
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/send", s.handleSend)
	return mux
}

type SendRequest struct {
	CallerID    string `json:"caller_id"`
	TargetOwner string `json:"target_owner"`
	Text        string `json:"text,omitempty"`
	MediaURL    string `json:"media_url,omitempty"`
}

type sendResponse struct {
	Status    string `json:"status"`
	ReceiptID string `json:"receipt_id"`
	Outcome   string `json:"outcome,omitempty"`
}

func (s *Server) handleSend(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.allowedSource(request.RemoteAddr) {
		http.Error(w, "untrusted source", http.StatusForbidden)
		return
	}
	if request.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	credential, ok := s.authenticate(request.Header.Get("Authorization"))
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
		return
	}
	idempotencyKey := request.Header.Get("Idempotency-Key")
	if !idempotencyKeyPattern.MatchString(idempotencyKey) {
		http.Error(w, "invalid idempotency key", http.StatusBadRequest)
		return
	}

	reader := http.MaxBytesReader(w, request.Body, maxSendBody)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var payload SendRequest
	if err := decoder.Decode(&payload); err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "trailing JSON data", http.StatusBadRequest)
		return
	}
	if payload.CallerID != credential.callerID {
		http.Error(w, "caller does not match token", http.StatusForbidden)
		return
	}
	if err := validateSendRequest(payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if (payload.Text != "" && !credential.scopes[ScopeSendText]) || (payload.MediaURL != "" && !credential.scopes[ScopeSendMedia]) {
		http.Error(w, "insufficient scope", http.StatusForbidden)
		return
	}
	if !s.delivery.HasOwner(payload.TargetOwner) {
		http.Error(w, "target owner is not available", http.StatusForbidden)
		return
	}
	fingerprint, err := sendRequestFingerprint(payload)
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	reservation, err := s.receipts.Reserve(credential.callerID, idempotencyKey, fingerprint)
	if errors.Is(err, ErrIdempotencyConflict) {
		http.Error(w, "idempotency key conflicts with another request", http.StatusConflict)
		return
	}
	if err != nil {
		log.Printf("[send-api] receipt reservation failed caller=%s", credential.callerID)
		http.Error(w, "receipt store unavailable", http.StatusServiceUnavailable)
		return
	}
	if reservation.Duplicate {
		writeSendResponse(w, http.StatusOK, sendResponse{Status: "duplicate", ReceiptID: shortReceiptID(reservation.ID), Outcome: reservation.Outcome})
		return
	}

	deliveryID := "api-" + shortReceiptID(reservation.ID)
	if payload.Text != "" {
		err = s.delivery.SendText(request.Context(), payload.TargetOwner, payload.Text, deliveryID)
	}
	if err == nil && payload.MediaURL != "" {
		err = s.delivery.SendMedia(request.Context(), payload.TargetOwner, payload.MediaURL)
	}
	if err != nil {
		if completeErr := s.receipts.Complete(reservation.ID, ReceiptFailed); completeErr != nil {
			log.Printf("[send-api] failure receipt persistence failed caller=%s receipt=%s", credential.callerID, shortReceiptID(reservation.ID))
		}
		log.Printf("[send-api] delivery failed caller=%s receipt=%s", credential.callerID, shortReceiptID(reservation.ID))
		http.Error(w, "delivery failed", http.StatusBadGateway)
		return
	}
	if err := s.receipts.Complete(reservation.ID, ReceiptSucceeded); err != nil {
		log.Printf("[send-api] success receipt persistence failed caller=%s receipt=%s", credential.callerID, shortReceiptID(reservation.ID))
		http.Error(w, "delivery outcome is uncertain", http.StatusInternalServerError)
		return
	}
	writeSendResponse(w, http.StatusOK, sendResponse{Status: "sent", ReceiptID: shortReceiptID(reservation.ID)})
}

func (s *Server) allowedSource(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if !s.proxyMode {
		return ip.IsLoopback()
	}
	for _, network := range s.trustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) authenticate(header string) (accessCredential, bool) {
	if !strings.HasPrefix(header, "Bearer ") {
		return accessCredential{}, false
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if token == "" || len(token) > 256 || strings.ContainsAny(token, " \t\r\n") {
		return accessCredential{}, false
	}
	digest := sha256.Sum256([]byte(token))
	match := -1
	for index, credential := range s.credentials {
		if subtle.ConstantTimeCompare(digest[:], credential.hash) == 1 {
			match = index
		}
	}
	if match < 0 {
		return accessCredential{}, false
	}
	return s.credentials[match], true
}

func validateSendRequest(request SendRequest) error {
	if !callerIDPattern.MatchString(request.CallerID) {
		return fmt.Errorf("caller_id is invalid")
	}
	if strings.TrimSpace(request.TargetOwner) == "" || len(request.TargetOwner) > 256 || strings.ContainsAny(request.TargetOwner, "\r\n\x00") {
		return fmt.Errorf("target_owner is invalid")
	}
	if request.Text == "" && request.MediaURL == "" {
		return fmt.Errorf("text or media_url is required")
	}
	if request.Text != "" {
		if !utf8.ValidString(request.Text) || utf8.RuneCountInString(request.Text) > maxTextRunes {
			return fmt.Errorf("text exceeds the size limit")
		}
	}
	if request.MediaURL != "" {
		if len(request.MediaURL) > maxMediaURL {
			return fmt.Errorf("media_url exceeds the size limit")
		}
		parsed, err := url.Parse(request.MediaURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return fmt.Errorf("media_url must be an HTTPS URL without credentials or fragment")
		}
	}
	return nil
}

func sendRequestFingerprint(request SendRequest) (string, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func shortReceiptID(id string) string {
	if len(id) <= 24 {
		return id
	}
	return id[:24]
}

func writeSendResponse(w http.ResponseWriter, status int, response sendResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
