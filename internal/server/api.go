package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/klauspost/fox/internal/optimize"
	"github.com/ncruces/zenity"
)

const (
	maxJSON   = 1 << 20
	maxUpload = 16 << 30
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body := http.MaxBytesReader(w, r.Body, maxJSON)
	err := json.NewDecoder(body).Decode(v)
	// net/http only notices a client disconnect (cancelling r.Context, which closes open dialogs) once the body hit EOF.
	if _, cerr := io.Copy(io.Discard, body); err == nil {
		err = cerr
	}
	if err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func (s *server) addPaths(paths []string) []int64 {
	ids := []int64{}
	for _, p := range paths {
		ids = append(ids, s.engine.Add(p, false)...)
	}
	return ids
}

// Native dialogs are variables so tests can stub them; they must never open real windows there.
var (
	selectFiles = func(ctx context.Context) ([]string, error) {
		return zenity.SelectFileMultiple(zenity.Context(ctx), zenity.Title("fox: add files"))
	}
	selectFolder = func(ctx context.Context) ([]string, error) {
		p, err := zenity.SelectFile(zenity.Context(ctx), zenity.Title("fox: add folder"), zenity.Directory())
		if p == "" {
			return nil, err
		}
		return []string{p}, err
	}
)

func (s *server) pick(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder bool `json:"folder"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !s.dialog.TryLock() {
		http.Error(w, "a file dialog is already open", http.StatusConflict)
		return
	}
	defer s.dialog.Unlock()

	pick := selectFiles
	if req.Folder {
		pick = selectFolder
	}
	paths, err := pick(r.Context())
	if err != nil && !errors.Is(err, zenity.ErrCanceled) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string][]int64{"ids": s.addPaths(paths)})
}

func (s *server) add(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var paths []string
	for _, p := range req.Paths {
		// Explorer's "Copy as path" wraps paths in quotes.
		if p = strings.Trim(strings.TrimSpace(p), `"'`); p != "" {
			paths = append(paths, p)
		}
	}
	writeJSON(w, map[string][]int64{"ids": s.addPaths(paths)})
}

// uploadName reduces a client supplied file name to a safe base name.
func uploadName(name string) (string, bool) {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == ".." || name == "/" || len(name) > 255 ||
		strings.ContainsFunc(name, unicode.IsControl) || !filepath.IsLocal(name) {
		return "", false
	}
	return name, true
}

func (s *server) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		if part.FormName() != "file" {
			continue
		}
		name, ok := uploadName(part.FileName())
		if !ok {
			http.Error(w, "invalid file name", http.StatusBadRequest)
			return
		}
		id, err := s.store(name, part)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]int64{"id": id})
		return
	}
}

// store saves an upload in its own directory, so the original name can be kept, and queues it.
func (s *server) store(name string, r io.Reader) (int64, error) {
	dir, err := os.MkdirTemp(s.uploadDir, "u")
	if err != nil {
		return 0, err
	}
	p := filepath.Join(dir, name)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = io.Copy(f, r)
		err = errors.Join(err, f.Close())
	}
	if err != nil {
		os.RemoveAll(dir)
		return 0, err
	}
	// Registering under the same lock as Add keeps a concurrent clear from missing a job that finished instantly.
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.engine.Add(p, true)
	if len(ids) != 1 {
		os.RemoveAll(dir)
		return 0, errors.New("upload could not be queued")
	}
	s.uploads[ids[0]] = p
	return ids[0], nil
}

func (s *server) download(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	p, ok := s.uploads[id]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if e, ok := s.engine.Job(id); !ok || e.State != optimize.Done {
		http.Error(w, "not finished", http.StatusConflict)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(p)}))
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func readID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	var req struct {
		ID int64 `json:"id"`
	}
	return req.ID, readJSON(w, r, &req)
}

func (s *server) cancel(w http.ResponseWriter, r *http.Request) {
	if id, ok := readID(w, r); ok {
		s.engine.Cancel(id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *server) remove(w http.ResponseWriter, r *http.Request) {
	if id, ok := readID(w, r); ok {
		s.forget(id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *server) clear(w http.ResponseWriter, r *http.Request) {
	s.forget(0)
	w.WriteHeader(http.StatusNoContent)
}

// forget removes finished jobs (all if id is 0) and the upload files the engine no longer knows about.
func (s *server) forget(id int64) {
	s.engine.Remove(id)
	s.mu.Lock()
	for jid, p := range s.uploads {
		if id != 0 && jid != id {
			continue
		}
		if _, ok := s.engine.Job(jid); !ok {
			os.RemoveAll(filepath.Dir(p))
			delete(s.uploads, jid)
		}
	}
	s.mu.Unlock()
	s.resyncClients()
}

func (s *server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.engine.Settings())
}

func (s *server) putSettings(w http.ResponseWriter, r *http.Request) {
	// Serialized, so concurrent saves from several tabs neither lose merges nor share Save's temp file.
	s.saving.Lock()
	defer s.saving.Unlock()
	st := s.engine.Settings()
	if !readJSON(w, r, st) {
		return
	}
	st.Clamp()
	s.engine.SetSettings(st)
	if err := st.Save(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, st)
}

func (s *server) listTools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.tools())
}
