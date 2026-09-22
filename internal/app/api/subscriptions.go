package api

import (
	"encoding/json"
	"errors"
	"net/http"

	userapp "github.com/antimage/antimage/internal/app/user"
)

func (s *Server) handleSubscriptionPath(w http.ResponseWriter, r *http.Request) {
	setSubscriptionNoCacheHeaders(w)
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, ok, err := s.userService.ResolveSubscriptionAlias(r.Context(), r.URL.Path, r.URL.Query())
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	s.handleResolvedSubscription(w, r, req)
}

func (s *Server) handleResolvedSubscription(w http.ResponseWriter, r *http.Request, req userapp.SubscriptionRenderRequest) {
	setSubscriptionNoCacheHeaders(w)
	req.UserAgent = r.Header.Get("User-Agent")
	req.DeviceType = r.Header.Get("X-AntiMage-Device-Type")
	req.DeviceManufacturer = r.Header.Get("X-AntiMage-Manufacturer")
	req.DeviceModel = r.Header.Get("X-AntiMage-Model")
	if req.DeviceModel == "" {
		req.DeviceModel = r.Header.Get("Sec-CH-UA-Model")
	}
	req.DevicePlatform = r.Header.Get("X-AntiMage-OS")
	if req.DevicePlatform == "" {
		req.DevicePlatform = r.Header.Get("Sec-CH-UA-Platform")
	}
	req.DevicePlatformVersion = r.Header.Get("X-AntiMage-OS-Version")
	if req.DevicePlatformVersion == "" {
		req.DevicePlatformVersion = r.Header.Get("Sec-CH-UA-Platform-Version")
	}
	req.DeviceClientName = r.Header.Get("X-AntiMage-Client")
	req.DeviceClientVersion = r.Header.Get("X-AntiMage-Client-Version")
	req.Accept = r.Header.Get("Accept")
	req.URL = requestAbsoluteURL(r)
	req.Start = r.URL.Query().Get("start")
	req.End = r.URL.Query().Get("end")
	req.ReadOnly = s.cfg.SubscriptionReadOnly
	if runtimeSettings, err := s.settingsRepo.RuntimeSettings(r.Context()); err == nil {
		req.ReadOnly = runtimeSettings.SubscriptionReadOnly
	}
	req.Usage = s.usageService

	switch req.ClientType {
	case "info":
		user, err := s.userService.SubscriptionInfo(r.Context(), req)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, user)
	case "usage":
		payload, err := s.userService.SubscriptionUsage(r.Context(), req)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, payload)
	default:
		response, err := s.userService.RenderSubscription(r.Context(), req)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		for key, value := range response.Headers {
			w.Header().Set(key, value)
		}
		if response.MediaType != "" {
			w.Header().Set("Content-Type", response.MediaType)
		}
		status := response.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if response.JSON != nil {
			_ = json.NewEncoder(w).Encode(response.JSON)
			return
		}
		_, _ = w.Write(response.Body)
	}
}

func setSubscriptionNoCacheHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0, private")
	w.Header().Set("CDN-Cache-Control", "no-store")
	w.Header().Set("Cloudflare-CDN-Cache-Control", "no-store")
	w.Header().Set("Expires", "0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set(
		"Accept-CH",
		"Sec-CH-UA-Platform, Sec-CH-UA-Platform-Version, Sec-CH-UA-Model",
	)
}

func writeSubscriptionError(w http.ResponseWriter, err error) {
	var mutationErr userapp.MutationError
	if errors.As(err, &mutationErr) {
		writeError(w, mutationErr.Status, mutationErr.Detail)
		return
	}
	writeError(w, http.StatusBadGateway, err.Error())
}

func requestAbsoluteURL(r *http.Request) string {
	origin := requestOrigin(r)
	if origin == "" {
		return r.URL.String()
	}
	return origin + r.URL.RequestURI()
}
