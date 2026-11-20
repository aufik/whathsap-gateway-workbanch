package web

import (
	"errors"
	"net/http"
	"strings"

	"whatshap-gateway/internal/auth"
	"whatshap-gateway/internal/database"
	"whatshap-gateway/internal/model"
	"whatshap-gateway/internal/wa"
)

// handleListDevices returns the caller's own WhatsApp channels with status.
func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized"))
		return
	}
	if s.auth.DB == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("database belum terhubung"))
		return
	}

	devices, err := s.auth.DB.ListWADevices(r.Context(), sess.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var statuses map[string]model.WAStatus
	if s.waPool != nil {
		statuses = s.waPool.Pool(sess.UserID).Statuses()
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices, "statuses": statuses})
}

func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized"))
		return
	}
	if s.auth.DB == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("database belum terhubung"))
		return
	}

	var req struct {
		DeviceName string `json:"deviceName"`
		Phone      string `json:"phone"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	phone := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, req.Phone)
	if len(phone) < 9 {
		writeErr(w, http.StatusBadRequest, errors.New("nomor WhatsApp tidak valid"))
		return
	}
	if req.DeviceName == "" {
		req.DeviceName = "WA " + phone[len(phone)-4:]
	}

	uuid, err := wa.NewDeviceUUID()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	device, err := s.auth.DB.AddWADevice(r.Context(), sess.UserID, req.DeviceName, phone, uuid)
	if err != nil {
		if database.IsUniqueViolation(err) {
			writeErr(w, http.StatusConflict, errors.New("nomor WhatsApp tersebut sudah terdaftar pada akun lain"))
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"device": device})
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized"))
		return
	}
	deviceUUID := r.PathValue("uuid")
	if deviceUUID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("uuid perangkat tidak valid"))
		return
	}

	if s.auth.DB != nil {
		devices, err := s.auth.DB.ListWADevices(r.Context(), sess.UserID)
		if err == nil {
			for _, d := range devices {
				if d.DeviceUUID == deviceUUID {
					if err := s.auth.DB.DeleteWADevice(r.Context(), sess.UserID, d.ID); err != nil {
						writeErr(w, http.StatusInternalServerError, err)
					}
					break
				}
			}
		}
	}
	if s.waPool != nil {
		s.waPool.DetachDevice(sess.UserID, deviceUUID)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSetUserWALimit(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized"))
		return
	}
	if sess.Role != "admin" {
		writeErr(w, http.StatusForbidden, errors.New("hanya admin yang dapat mengubah batas perangkat"))
		return
	}
	if s.auth.DB == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("database belum terhubung"))
		return
	}

	var req struct {
		UserID int64 `json:"userId"`
		Limit  int   `json:"limit"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.UserID == 0 {
		req.UserID = sess.UserID
	}
	if err := s.auth.DB.SetMaxWADevices(r.Context(), req.UserID, req.Limit); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"userId": req.UserID, "limit": req.Limit})
}
