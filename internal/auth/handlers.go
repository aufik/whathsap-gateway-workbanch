package auth

import (
	"encoding/json"
	"errors"
	"net/http"

	"whatshap-gateway/internal/database"
)

type Handler struct {
	Store   *database.Store
	Manager *Manager
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func decode(r *http.Request, dst any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}

func (h *Handler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	user, err := h.Store.Authenticate(r.Context(), req.Username, req.Password)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, errors.New("username atau password salah"))
		return
	}
	if err := h.Manager.Login(r.Context(), w, user.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	h.Manager.Logout(r.Context(), w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, ok := FromContext(r.Context())
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

func (h *Handler) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.Store.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (h *Handler) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		FullName string `json:"fullName"`
		Role     string `json:"role"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Username) < 3 || len(req.Password) < 5 {
		writeErr(w, http.StatusBadRequest, errors.New("username minimal 3 karakter dan password minimal 5 karakter"))
		return
	}
	user, err := h.Store.CreateUser(r.Context(), req.Username, req.Password, req.FullName, req.Role)
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

// RegisterProtected registers handlers that require authentication.
func (h *Handler) RegisterProtected(mux *http.ServeMux) {
	// User management routes are registered by the web layer to attach tenant context.
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	h.handleMe(w, r)
}

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	h.handleListUsers(w, r)
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	h.handleCreateUser(w, r)
}
