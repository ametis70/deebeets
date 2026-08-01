package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
)

// Server serves the control API on a Unix socket.
type Server struct {
	ctrl       Controller
	socketPath string
	httpServer *http.Server
	listener   net.Listener
}

// NewServer builds a control server bound to socketPath.
func NewServer(ctrl Controller, socketPath string) *Server {
	return &Server{ctrl: ctrl, socketPath: socketPath}
}

// Listen creates the Unix socket listener (removing any stale socket first).
func (s *Server) Listen() error {
	if err := os.Remove(s.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	s.listener = ln

	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("POST /sync", s.handleSync)
	mux.HandleFunc("POST /download", s.handleDownload)
	mux.HandleFunc("POST /redownload", s.handleRedownload)
	mux.HandleFunc("POST /start", s.handleStart)
	mux.HandleFunc("POST /stop", s.handleStop)
	mux.HandleFunc("GET /blocklist", s.handleBlocklistList)
	mux.HandleFunc("POST /blocklist", s.handleBlocklistAdd)
	mux.HandleFunc("DELETE /blocklist", s.handleBlocklistRemove)
	mux.HandleFunc("POST /beets/import", s.handleBeetsImport)
	mux.HandleFunc("GET /items", s.handleItems)
	s.httpServer = &http.Server{Handler: mux}
	return nil
}

// Serve blocks serving requests until the listener is closed.
func (s *Server) Serve() error {
	err := s.httpServer.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close shuts the server down and removes the socket.
func (s *Server) Close(ctx context.Context) error {
	if s.httpServer != nil {
		_ = s.httpServer.Shutdown(ctx)
	}
	return os.Remove(s.socketPath)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, APIResponse{OK: false, Error: err.Error()})
}

// decode reads a JSON request body, tolerating an empty body (optional payload).
func decode(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	err := json.NewDecoder(r.Body).Decode(v)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.ctrl.Status(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	var req SyncRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.ctrl.Sync(r.Context(), req.Selection)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	var req DownloadRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	n, err := s.ctrl.Download(r.Context(), req.Kind, req.IDs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, CountResponse{Count: n})
}

func (s *Server) handleRedownload(w http.ResponseWriter, r *http.Request) {
	var req RedownloadRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	n, err := s.ctrl.Redownload(r.Context(), req.Mode, req.IDs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, CountResponse{Count: n})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if err := s.ctrl.StartDownload(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, APIResponse{OK: true, Message: "download stage started"})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if err := s.ctrl.StopDownload(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, APIResponse{OK: true, Message: "download stage stopped"})
}

func (s *Server) handleBlocklistAdd(w http.ResponseWriter, r *http.Request) {
	var req BlocklistRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.ctrl.BlocklistAdd(r.Context(), req.Kind, req.IDs, req.Reason); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, APIResponse{OK: true})
}

func (s *Server) handleBlocklistRemove(w http.ResponseWriter, r *http.Request) {
	var req BlocklistRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.ctrl.BlocklistRemove(r.Context(), req.Kind, req.IDs); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, APIResponse{OK: true})
}

func (s *Server) handleBlocklistList(w http.ResponseWriter, r *http.Request) {
	blocks, err := s.ctrl.BlocklistList(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, blocks)
}

func (s *Server) handleBeetsImport(w http.ResponseWriter, r *http.Request) {
	var req BeetsImportRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.ctrl.BeetsImport(r.Context(), req.Path); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, APIResponse{OK: true, Message: "import triggered"})
}

func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	var states []string
	if q := r.URL.Query().Get("state"); q != "" {
		states = splitCSV(q)
	}
	limit := 0 // 0 = unlimited
	items, err := s.ctrl.Items(r.Context(), states, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, ItemsResponse{Items: items})
}
