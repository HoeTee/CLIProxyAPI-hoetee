package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagehistory"
	log "github.com/sirupsen/logrus"
)

func (s *Server) getUsageHistorySummary(c *gin.Context) {
	if s.usageHistory == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage history is unavailable"})
		return
	}
	filter, err := usageHistoryFilter(c, time.Now())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	result, err := s.usageHistory.Summary(c.Request.Context(), filter)
	if err != nil {
		usageHistoryQueryError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func (s *Server) getUsageHistoryRequests(c *gin.Context) {
	if s.usageHistory == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage history is unavailable"})
		return
	}
	filter, err := usageHistoryFilter(c, time.Now())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	result, err := s.usageHistory.Requests(c.Request.Context(), filter)
	if err != nil {
		usageHistoryQueryError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func usageHistoryQueryError(c *gin.Context, err error) {
	log.WithError(err).Warn("usage history: query failed")
	c.JSON(http.StatusInternalServerError, gin.H{"error": "usage history query failed"})
}

func usageHistoryFilter(c *gin.Context, now time.Time) (usagehistory.Filter, error) {
	f := usagehistory.Filter{
		Provider: strings.TrimSpace(c.Query("provider")), Client: strings.TrimSpace(c.Query("client")),
		Model: strings.TrimSpace(c.Query("model")), Outcome: strings.TrimSpace(c.Query("outcome")),
		RequestID: strings.TrimSpace(c.Query("request_id")), Bucket: strings.TrimSpace(c.Query("bucket")),
		To: now, Limit: 50,
	}
	for _, field := range []string{f.Provider, f.Client, f.Model, f.RequestID} {
		if len(field) > 512 {
			return f, fmt.Errorf("filter value is too long")
		}
	}
	if raw := c.Query("to"); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return f, fmt.Errorf("to must be an RFC3339 date and time")
		}
		f.To = value
	}
	f.From = f.To.Add(-24 * time.Hour)
	if raw := c.Query("from"); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return f, fmt.Errorf("from must be an RFC3339 date and time")
		}
		f.From = value
	}
	if !f.From.Before(f.To) || f.To.Sub(f.From) > 400*24*time.Hour {
		return f, fmt.Errorf("the date range must be positive and at most 400 days")
	}
	switch f.Outcome {
	case "", "succeeded", "failed", "rejected", "canceled", "imported":
	default:
		return f, fmt.Errorf("invalid outcome")
	}
	if f.Bucket == "" {
		switch {
		case f.To.Sub(f.From) <= 24*time.Hour:
			f.Bucket = "hour"
		case f.To.Sub(f.From) <= 31*24*time.Hour:
			f.Bucket = "day"
		default:
			f.Bucket = "month"
		}
	}
	switch f.Bucket {
	case "hour":
		if f.To.Sub(f.From) > 31*24*time.Hour {
			return f, fmt.Errorf("hourly data is limited to 31 days")
		}
	case "day", "month":
	default:
		return f, fmt.Errorf("invalid bucket")
	}
	if raw := c.Query("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			return f, fmt.Errorf("limit must be an integer between 1 and 100")
		}
		f.Limit = value
	}
	if raw := c.Query("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return f, fmt.Errorf("offset must be a nonnegative integer")
		}
		f.Offset = value
	}
	return f, nil
}
