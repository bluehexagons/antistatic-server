package main

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"mime"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

var eventIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{15,79}$`)
var coarseIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var archPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
var reasonCodePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var symbolPattern = regexp.MustCompile(`^[A-Za-z0-9_:.<>~()+*,& -]{1,120}$`)
var reportIDPattern = regexp.MustCompile(`^(cr|fb|nr)-[a-f0-9]{16}$`)

type crashRequest struct {
	clientIdentity
	EventID    string   `json:"event_id"`
	AppVersion string   `json:"-"`
	Platform   string   `json:"platform"`
	Arch       string   `json:"arch"`
	ReasonCode string   `json:"reason_code"`
	Symbols    []string `json:"symbols"`
}

type feedbackRequest struct {
	clientIdentity
	EventID         string `json:"event_id"`
	Category        string `json:"category"`
	Subject         string `json:"subject"`
	Body            string `json:"body"`
	RelatedReportID string `json:"related_report_id,omitempty"`
}

type performanceRequest struct {
	clientIdentity
	EventID          string  `json:"event_id"`
	Platform         string  `json:"platform"`
	Arch             string  `json:"arch"`
	RendererFamily   string  `json:"renderer_family"`
	GPUVendor        string  `json:"gpu_vendor"`
	MemoryGiBBucket  string  `json:"memory_gib_bucket"`
	CPUCoresBucket   string  `json:"cpu_cores_bucket"`
	ResolutionBucket string  `json:"resolution_bucket"`
	SampleFrames     int     `json:"sample_frames"`
	FrameMsAvg       float64 `json:"frame_ms_avg"`
	FrameMsP95       float64 `json:"frame_ms_p95"`
}

type reportResponse struct {
	ReportID string `json:"report_id"`
}

func oneOf(value string, allowed ...string) bool {
	return slices.Contains(allowed, value)
}

func validEventID(value string) bool {
	return eventIDPattern.MatchString(value)
}

func validPlatform(value string) bool {
	return oneOf(value, "windows", "linux", "macos", "steamdeck", "unknown")
}

func validArch(value string) bool {
	return archPattern.MatchString(value)
}

func validTextLength(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func (request crashRequest) valid() bool {
	if !validEventID(request.EventID) || !validPlatform(request.Platform) || !validArch(request.Arch) || !reasonCodePattern.MatchString(request.ReasonCode) {
		return false
	}
	if request.Symbols == nil || len(request.Symbols) > 48 {
		return false
	}
	for _, symbol := range request.Symbols {
		if strings.ContainsAny(symbol, `/\\`) || !symbolPattern.MatchString(symbol) {
			return false
		}
	}
	return true
}

func (request feedbackRequest) valid() bool {
	return validEventID(request.EventID) &&
		oneOf(request.Category, "bug", "feedback", "other") &&
		request.Subject != "" && validTextLength(request.Subject, 120) &&
		request.Body != "" && validTextLength(request.Body, 6000) &&
		(request.RelatedReportID == "" || reportIDPattern.MatchString(request.RelatedReportID))
}

func (request performanceRequest) valid() bool {
	return validEventID(request.EventID) && validPlatform(request.Platform) && validArch(request.Arch) &&
		oneOf(request.RendererFamily, "opengl", "vulkan", "metal", "direct3d11", "direct3d12", "webgl", "other", "unknown") &&
		oneOf(request.GPUVendor, "amd", "intel", "nvidia", "apple", "qualcomm", "arm", "imagination", "other", "unknown") &&
		oneOf(request.MemoryGiBBucket, "under-4", "4-7", "8-15", "16-31", "32-63", "64-plus", "unknown") &&
		oneOf(request.CPUCoresBucket, "1-2", "3-4", "5-8", "9-16", "17-plus", "unknown") &&
		oneOf(request.ResolutionBucket, "720p-or-less", "1080p", "1440p", "2160p-or-more", "other", "unknown") &&
		request.SampleFrames > 0 && request.SampleFrames <= 1000000 &&
		!math.IsNaN(request.FrameMsAvg) && !math.IsInf(request.FrameMsAvg, 0) &&
		!math.IsNaN(request.FrameMsP95) && !math.IsInf(request.FrameMsP95, 0) &&
		request.FrameMsAvg > 0 && request.FrameMsAvg <= 1000 &&
		request.FrameMsP95 >= request.FrameMsAvg && request.FrameMsP95 <= 1000
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, destination any) int {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return http.StatusUnsupportedMediaType
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return http.StatusRequestEntityTooLarge
		}
		return http.StatusBadRequest
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return http.StatusRequestEntityTooLarge
		}
		return http.StatusBadRequest
	}
	return 0
}

func writeIngestError(w http.ResponseWriter, status int) {
	message := "Invalid report"
	switch status {
	case http.StatusUnsupportedMediaType:
		message = "Content-Type must be application/json"
	case http.StatusRequestEntityTooLarge:
		message = "Request body too large"
	case http.StatusServiceUnavailable:
		message = "Report storage unavailable"
	case http.StatusUpgradeRequired:
		message = "Report endpoints require HTTPS"
	case http.StatusTooManyRequests:
		message = "Report rate limit exceeded"
	}
	http.Error(w, message, status)
}

type reportAPI struct {
	store   *reportStore
	limiter *rateLimiter
	config  Config
}

func (api reportAPI) validateRequest(w http.ResponseWriter, r *http.Request) bool {
	if !requestIsHTTPS(r) {
		clientIP := net.ParseIP(getClientIP(r))
		if clientIP == nil || !clientIP.IsLoopback() {
			writeIngestError(w, http.StatusUpgradeRequired)
			return false
		}
	}
	if api.limiter != nil && !api.limiter.allow(getClientIP(r)+"|"+r.URL.Path) {
		writeIngestError(w, http.StatusTooManyRequests)
		return false
	}
	if api.store == nil {
		writeIngestError(w, http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (api reportAPI) crash(w http.ResponseWriter, r *http.Request) {
	if !api.validateRequest(w, r) {
		return
	}
	var request crashRequest
	if status := decodeStrictJSON(w, r, &request); status != 0 {
		writeIngestError(w, status)
		return
	}
	if !validateClientIdentity(w, api.config, request.clientIdentity) {
		return
	}
	if !request.valid() {
		writeIngestError(w, http.StatusBadRequest)
		return
	}
	request.AppVersion = request.ClientVersion
	id, _, err := api.store.appendCrash(request)
	if err != nil {
		slog.Error("Crash report storage failed", "error", err)
		writeIngestError(w, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(reportResponse{ReportID: id})
}

func (api reportAPI) feedback(w http.ResponseWriter, r *http.Request) {
	if !api.validateRequest(w, r) {
		return
	}
	var request feedbackRequest
	if status := decodeStrictJSON(w, r, &request); status != 0 {
		writeIngestError(w, status)
		return
	}
	if !validateClientIdentity(w, api.config, request.clientIdentity) {
		return
	}
	if !request.valid() {
		writeIngestError(w, http.StatusBadRequest)
		return
	}
	id, _, err := api.store.appendFeedback(request, request.ClientVersion)
	if err != nil {
		slog.Error("Feedback storage failed", "error", err)
		writeIngestError(w, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(reportResponse{ReportID: id})
}

func (api reportAPI) performance(w http.ResponseWriter, r *http.Request) {
	if !api.validateRequest(w, r) {
		return
	}
	var request performanceRequest
	if status := decodeStrictJSON(w, r, &request); status != 0 {
		writeIngestError(w, status)
		return
	}
	if !validateClientIdentity(w, api.config, request.clientIdentity) {
		return
	}
	if !request.valid() {
		writeIngestError(w, http.StatusBadRequest)
		return
	}
	if _, err := api.store.appendPerformance(request, request.ClientVersion); err != nil {
		slog.Error("Performance metric storage failed", "error", err)
		writeIngestError(w, http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
