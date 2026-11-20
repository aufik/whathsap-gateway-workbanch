package web

import (
	"errors"
	"net/http"

	"whatshap-gateway/internal/auth"
	"whatshap-gateway/internal/database"
)

func (s *Server) currentSession(r *http.Request) (*database.Session, bool) {
	return auth.FromContext(r.Context())
}

func (s *Server) handleCurrentUser(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":       sess.UserID,
		"username": sess.Username,
		"fullName": sess.FullName,
		"role":     sess.Role,
	})
}

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized"))
		return
	}
	if sess.Role != "admin" {
		writeErr(w, http.StatusForbidden, errors.New("hanya admin yang dapat mengakses daftar pengguna"))
		return
	}
	if s.auth.DB == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("database belum terhubung"))
		return
	}
	users, err := s.auth.DB.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized"))
		return
	}
	if sess.Role != "admin" {
		writeErr(w, http.StatusForbidden, errors.New("hanya admin yang dapat membuat pengguna baru"))
		return
	}
	if s.auth.DB == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("database belum terhubung"))
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		FullName string `json:"fullName"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Username) < 3 || len(req.Password) < 5 {
		writeErr(w, http.StatusBadRequest, errors.New("username minimal 3 karakter dan password minimal 5 karakter"))
		return
	}
	user, err := s.auth.DB.CreateUser(r.Context(), req.Username, req.Password, req.FullName, req.Role)
	if err != nil {
		if database.IsUniqueViolation(err) {
			writeErr(w, http.StatusConflict, errors.New("username sudah dipakai"))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": user})
}
