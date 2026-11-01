package management

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/generations"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/snapshots"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// StoreReader is what the quota and generations routes read from the
// store.
type StoreReader interface {
	Quota(ctx context.Context) ([]generations.CredentialUsage, error)
	RefreshUsage(ctx context.Context, credentialID string) (string, generations.CredentialUsage, error)
	Timeline(ctx context.Context, credentialID string, from, to time.Time) ([]generations.TimelinePoint, error)
	Generations(ctx context.Context, f generations.GenerationFilter) (generations.GenerationPage, error)
	Generation(ctx context.Context, id uuid.UUID) (generations.GenerationDetail, error)
	Payload(ctx context.Context, id uuid.UUID, kind string) (generations.PayloadBody, error)
	Summary(ctx context.Context, q generations.SummaryQuery) ([]generations.Bucket, error)
	Latency(ctx context.Context, from, to time.Time) ([]generations.Latency, error)
}

// Defaults of the store routes' ranges.
const (
	defaultTimelineRange = 7 * 24 * time.Hour
	defaultSummaryDays   = 30
)

// Headers of the payload route.
const (
	// HeaderPayloadRebuilt marks a pruned input returned rebuilt as the
	// RFC 8785 serialization of the body as sent.
	HeaderPayloadRebuilt = "X-Payload-Rebuilt"
	// HeaderPayloadTruncated marks a body stored only up to the payload
	// limit.
	HeaderPayloadTruncated = "X-Payload-Truncated"
)

// SetStore sets the store the quota and generations routes read, without
// which they answer 503, and the URL template, with {trace_id} where the
// trace ID goes, that gives each generation with a trace its trace_url.
func (h *Handler) SetStore(store StoreReader, traceURLTemplate string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.store = store
	h.traceURLTemplate = traceURLTemplate
	h.mu.Unlock()
}

// traceURL renders a generation's trace link, or "" without a template or
// a trace ID.
func (h *Handler) traceURL(traceID string) string {
	h.mu.Lock()
	template := h.traceURLTemplate
	h.mu.Unlock()
	if template == "" || traceID == "" {
		return ""
	}
	return strings.ReplaceAll(template, "{trace_id}", url.PathEscape(traceID))
}

// storeOrUnavailable returns the store, or answers 503 when there is none.
func (h *Handler) storeOrUnavailable(c *gin.Context) StoreReader {
	h.mu.Lock()
	store := h.store
	h.mu.Unlock()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "the store is not configured"})
	}
	return store
}

// storeError answers a failed store read without its message, which can
// hold SQL values, unless the request itself was refused.
func storeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, generations.ErrInvalidQuery):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, generations.ErrNotFound), errors.Is(err, snapshots.ErrUnknownCredential):
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	case errors.Is(err, snapshots.ErrNoUsageSource):
		c.JSON(http.StatusConflict, gin.H{"error": snapshots.ErrNoUsageSource.Error()})
	case errors.Is(err, snapshots.ErrPollRunning):
		c.JSON(http.StatusConflict, gin.H{"error": snapshots.ErrPollRunning.Error()})
	case errors.Is(err, generations.ErrNoPoller):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": generations.ErrNoPoller.Error()})
	case errors.Is(err, generations.ErrRebuild):
		c.JSON(http.StatusInternalServerError, gin.H{"error": generations.ErrRebuild.Error()})
	case errors.Is(err, context.DeadlineExceeded):
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "the store did not answer in time"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the store failed to answer"})
	}
}

func invalid(c *gin.Context, message string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": message})
}

// timeParam reads an RFC 3339 time, or returns fallback when absent.
func timeParam(c *gin.Context, name string, fallback time.Time) (time.Time, bool) {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return fallback, true
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		invalid(c, name+" must be an RFC 3339 time")
		return time.Time{}, false
	}
	return t, true
}

// dayParam reads a YYYY-MM-DD day, or returns fallback when absent.
func dayParam(c *gin.Context, name string, fallback time.Time) (time.Time, bool) {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return fallback, true
	}
	t, err := time.Parse(time.DateOnly, value)
	if err != nil {
		invalid(c, name+" must be a day as YYYY-MM-DD")
		return time.Time{}, false
	}
	return t, true
}

// dayRangeParams reads the from and to days, by default the last
// defaultSummaryDays UTC days through today.
func dayRangeParams(c *gin.Context) (time.Time, time.Time, bool) {
	today := time.Now().UTC()
	to, ok := dayParam(c, "to", today)
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	from, ok := dayParam(c, "from", to.AddDate(0, 0, -(defaultSummaryDays-1)))
	return from, to, ok
}

// GetQuota lists every credential with its latest usage snapshot.
func (h *Handler) GetQuota(c *gin.Context) {
	store := h.storeOrUnavailable(c)
	if store == nil {
		return
	}
	credentials, err := store.Quota(c.Request.Context())
	if err != nil {
		storeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"credentials": credentials})
}

// RefreshQuota polls one credential's usage source now.
func (h *Handler) RefreshQuota(c *gin.Context) {
	store := h.storeOrUnavailable(c)
	if store == nil {
		return
	}
	var req struct {
		CredentialID string `json:"credential_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.CredentialID) == "" {
		invalid(c, "the body must name a credential_id")
		return
	}
	outcome, credential, err := store.RefreshUsage(c.Request.Context(), req.CredentialID)
	if err != nil {
		storeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"outcome": outcome, "credential": credential})
}

// GetQuotaTimeline returns one credential's meter history.
func (h *Handler) GetQuotaTimeline(c *gin.Context) {
	store := h.storeOrUnavailable(c)
	if store == nil {
		return
	}
	credential := strings.TrimSpace(c.Query("credential"))
	if credential == "" {
		invalid(c, "credential is required")
		return
	}
	to, ok := timeParam(c, "to", time.Now())
	if !ok {
		return
	}
	from, ok := timeParam(c, "from", to.Add(-defaultTimelineRange))
	if !ok {
		return
	}
	points, err := store.Timeline(c.Request.Context(), credential, from, to)
	if err != nil {
		storeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"credential_id": credential, "points": points})
}

// ListGenerations lists generations newest first.
func (h *Handler) ListGenerations(c *gin.Context) {
	store := h.storeOrUnavailable(c)
	if store == nil {
		return
	}
	from, ok := timeParam(c, "from", time.Time{})
	if !ok {
		return
	}
	to, ok := timeParam(c, "to", time.Time{})
	if !ok {
		return
	}
	f := generations.GenerationFilter{From: from, To: to, Provider: c.Query("provider"), CredentialID: c.Query("credential"),
		Model: c.Query("model"), Effort: c.Query("effort"), ClientFingerprint: c.Query("client"),
		ConversationID: c.Query("conversation"), Status: c.Query("status"), Cursor: c.Query("cursor")}
	if limit := c.Query("limit"); limit != "" {
		n, err := strconv.Atoi(limit)
		if err != nil || n < 1 {
			invalid(c, "limit must be a positive integer")
			return
		}
		f.Limit = n
	}
	page, err := store.Generations(c.Request.Context(), f)
	if err != nil {
		storeError(c, err)
		return
	}
	for i := range page.Generations {
		page.Generations[i].TraceURL = h.traceURL(page.Generations[i].TraceID)
	}
	c.JSON(http.StatusOK, page)
}

func generationID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		invalid(c, "the generation id must be a UUID")
		return uuid.UUID{}, false
	}
	return id, true
}

// GetGeneration returns one generation.
func (h *Handler) GetGeneration(c *gin.Context) {
	store := h.storeOrUnavailable(c)
	if store == nil {
		return
	}
	id, ok := generationID(c)
	if !ok {
		return
	}
	detail, err := store.Generation(c.Request.Context(), id)
	if err != nil {
		storeError(c, err)
		return
	}
	detail.TraceURL = h.traceURL(detail.TraceID)
	c.JSON(http.StatusOK, detail)
}

// GetGenerationPayload returns one payload of a generation decompressed,
// with the content type it was sent with.
func (h *Handler) GetGenerationPayload(c *gin.Context) {
	store := h.storeOrUnavailable(c)
	if store == nil {
		return
	}
	id, ok := generationID(c)
	if !ok {
		return
	}
	payload, err := store.Payload(c.Request.Context(), id, c.Param("kind"))
	if err != nil {
		storeError(c, err)
		return
	}
	if payload.Rebuilt {
		c.Header(HeaderPayloadRebuilt, "true")
	}
	if payload.Truncated {
		c.Header(HeaderPayloadTruncated, "true")
	}
	// A stored body carries the content type its sender chose; these keep
	// it inert wherever it is opened.
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "sandbox")
	c.Data(http.StatusOK, payload.ContentType, payload.Body)
}

// GetGenerationsSummary returns daily usage buckets.
func (h *Handler) GetGenerationsSummary(c *gin.Context) {
	store := h.storeOrUnavailable(c)
	if store == nil {
		return
	}
	from, to, ok := dayRangeParams(c)
	if !ok {
		return
	}
	var groupBy []string
	for _, dim := range strings.Split(c.Query("group_by"), ",") {
		if dim = strings.TrimSpace(dim); dim != "" {
			groupBy = append(groupBy, dim)
		}
	}
	buckets, err := store.Summary(c.Request.Context(), generations.SummaryQuery{From: from, To: to, GroupBy: groupBy})
	if err != nil {
		storeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly), "buckets": buckets})
}

// GetGenerationsLatency returns daily time-to-first-token percentiles per
// upstream model.
func (h *Handler) GetGenerationsLatency(c *gin.Context) {
	store := h.storeOrUnavailable(c)
	if store == nil {
		return
	}
	from, to, ok := dayRangeParams(c)
	if !ok {
		return
	}
	latency, err := store.Latency(c.Request.Context(), from, to)
	if err != nil {
		storeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly), "latency": latency})
}
