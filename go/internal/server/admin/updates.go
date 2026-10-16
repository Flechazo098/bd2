package admin

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"bd2server/internal/server/platform/updates"
)

// Updates is the server's release service as the console uses it.
type Updates interface {
	Status() updates.Status
	CheckNow()
	Schedule(delay time.Duration, confirmIrreversible bool, by string) error
	ScheduleSchedules(delay time.Duration, by string) error
	Cancel(by string) error
}

// AttachUpdates enables the owner's update page.
func (c *Console) AttachUpdates(service Updates) {
	c.mu.Lock()
	c.updates = service
	c.mu.Unlock()
}

func (c *Console) updateService(w http.ResponseWriter) (Updates, bool) {
	c.mu.Lock()
	service := c.updates
	c.mu.Unlock()
	if service == nil {
		fail(w, http.StatusNotFound, "unsupported", "update service unavailable")
	}
	return service, service != nil
}

func (c *Console) registerUpdates(mux *http.ServeMux, owner func(session) bool) {
	mux.HandleFunc("GET /admin/api/updates", c.require(owner, func(w http.ResponseWriter, _ *http.Request, _ session) {
		if service, ok := c.updateService(w); ok {
			writeJSON(w, http.StatusOK, service.Status())
		}
	}))
	mux.HandleFunc("POST /admin/api/updates/check", c.require(owner, func(w http.ResponseWriter, _ *http.Request, _ session) {
		if service, ok := c.updateService(w); ok {
			service.CheckNow()
			writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
		}
	}))
	mux.HandleFunc("POST /admin/api/updates/apply", c.require(owner, func(w http.ResponseWriter, r *http.Request, current session) {
		service, ok := c.updateService(w)
		if !ok {
			return
		}
		var request struct {
			Component           string `json:"component"`
			DelayMinutes        int    `json:"delay_minutes"`
			ConfirmIrreversible bool   `json:"confirm_irreversible"`
		}
		if !decode(w, r, &request) {
			return
		}
		by := "console:" + current.AccountID
		var err error
		switch request.Component {
		case "", updates.ComponentServer:
			err = service.Schedule(time.Duration(request.DelayMinutes)*time.Minute, request.ConfirmIrreversible, by)
		case updates.ComponentSchedules:
			err = service.ScheduleSchedules(time.Duration(request.DelayMinutes)*time.Minute, by)
		default:
			fail(w, http.StatusBadRequest, "unsupported", "unknown update component")
			return
		}
		if err != nil {
			rejected(w, err)
			return
		}
		slog.Info("update scheduled from console", "delay_minutes", request.DelayMinutes, "by", by)
		writeJSON(w, http.StatusAccepted, service.Status())
	}))
	mux.HandleFunc("POST /admin/api/updates/cancel", c.require(owner, func(w http.ResponseWriter, _ *http.Request, current session) {
		service, ok := c.updateService(w)
		if !ok {
			return
		}
		if err := service.Cancel("console:" + current.AccountID); err != nil {
			rejected(w, err)
			return
		}
		writeJSON(w, http.StatusOK, service.Status())
	}))
}

// rejected answers with the rejection's code so the console can translate it.
func rejected(w http.ResponseWriter, err error) {
	var rejection *updates.Rejection
	if errors.As(err, &rejection) {
		fail(w, http.StatusConflict, rejection.Code, rejection.Message)
		return
	}
	fail(w, http.StatusInternalServerError, "unavailable", err.Error())
}
