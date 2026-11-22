package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"whatshap-gateway/internal/auth"
	"whatshap-gateway/internal/model"
)

// userSettings returns the caller's own behaviour settings. Falls back to the
// process defaults on a first visit or when the database is unavailable.
func (s *Server) userSettings(ctx context.Context, userID int64) model.HumanSettings {
	base := s.cfg.GetSettings()
	if s.auth.DB == nil || s.auth.DB.DB() == nil || userID <= 0 {
		return base
	}
	raw, botPhone, ok, err := s.auth.DB.LoadSettings(ctx, userID)
	if err != nil || !ok {
		return base
	}
	stored := base
	if err := json.Unmarshal(raw, &stored); err != nil {
		return base
	}
	if botPhone != "" {
		stored.BotPhone = botPhone
	}
	return stored
}

func (s *Server) saveUserSettings(ctx context.Context, userID int64, next model.HumanSettings) error {
	if s.auth.DB == nil || userID <= 0 {
		return errors.New("database tidak tersedia untuk menyimpan pengaturan")
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	return s.auth.DB.SaveSettings(ctx, userID, raw, next.BotPhone)
}

func (s *Server) handleUpdateSettingsForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	var next model.HumanSettings
	if err := decodeJSON(r, &next); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	current := s.userSettings(ctx, userID)
	next = mergeSettings(current, next)
	if err := s.saveUserSettings(ctx, userID, next); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.tenantRunner(ctx, userID).SetSettings(userID, next)
	if sess, ok := auth.FromContext(ctx); ok {
		_ = sess
	}
	s.broker.Broadcast(userID, "settings_updated", next)
	writeJSON(w, http.StatusOK, next)
}

// mergeSettings keeps request-scoped edits without letting a partial payload
// wipe unrelated fields (the UI always sends the full form, but be explicit).
func mergeSettings(base, next model.HumanSettings) model.HumanSettings {
	if next.BotPhone == "" {
		next.BotPhone = base.BotPhone
	}
	if next.QuestionMode == "" {
		next.QuestionMode = base.QuestionMode
	}
	return next
}
