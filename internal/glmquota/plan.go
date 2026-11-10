package glmquota

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const CodingURL = "https://open.bigmodel.cn/api/coding/paas/v4"
const monitorURL = "https://open.bigmodel.cn/api/monitor/usage/quota/limit"

func IsOfficialEndpoint(baseURL string) bool {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") == CodingURL
}

// Credential accepts an environment secret only for the official endpoint.
func Credential(baseURL, configured string) string {
	if configured == "" && IsOfficialEndpoint(baseURL) {
		return strings.TrimSpace(os.Getenv("GLM_CODING_PLAN_API_KEY"))
	}
	return configured
}

type Plan struct {
	Level   string                `json:"level"`
	Windows map[string]PlanWindow `json:"windows"`
}

type PlanWindow struct {
	Limit            *float64   `json:"limit_credits,omitempty"`
	Used             *float64   `json:"used_credits,omitempty"`
	Remaining        *float64   `json:"remaining_credits,omitempty"`
	RemainingPercent *float64   `json:"remaining_percent,omitempty"`
	ResetAt          *time.Time `json:"reset_at,omitempty"`
}

func FetchPlan(ctx context.Context, key string) (_ Plan, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, monitorURL, nil)
	if err != nil {
		return Plan{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return Plan{}, err
	}
	defer func() {
		if errClose := res.Body.Close(); errClose != nil && err == nil {
			err = errClose
		}
	}()
	if res.StatusCode != http.StatusOK {
		return Plan{}, errors.New("GLM quota endpoint rejected the request")
	}
	return parsePlan(io.LimitReader(res.Body, 64<<10))
}

func parsePlan(body io.Reader) (Plan, error) {
	var result struct {
		Code    int  `json:"code"`
		Success bool `json:"success"`
		Data    struct {
			Level  string `json:"level"`
			Limits []struct {
				Type       string   `json:"type"`
				Unit       int      `json:"unit"`
				Number     int      `json:"number"`
				Limit      *float64 `json:"usage"`
				Used       *float64 `json:"currentValue"`
				Remaining  *float64 `json:"remaining"`
				Percentage *float64 `json:"percentage"`
				Reset      int64    `json:"nextResetTime"`
			} `json:"limits"`
		} `json:"data"`
	}
	if err := json.NewDecoder(body).Decode(&result); err != nil || !result.Success || result.Code != http.StatusOK {
		return Plan{}, errors.New("invalid GLM quota response")
	}
	plan := Plan{Level: result.Data.Level, Windows: make(map[string]PlanWindow)}
	for _, item := range result.Data.Limits {
		if item.Type != "CREDIT_LIMIT" {
			continue
		}
		var name string
		// These unit and number pairs are from the domestic Coding Plan response.
		switch {
		case item.Unit == 3 && item.Number == 5:
			name = "five_hour"
		case item.Unit == 6 && item.Number == 1:
			name = "weekly"
		default:
			continue
		}
		window := PlanWindow{Limit: nonnegative(item.Limit), Used: nonnegative(item.Used), Remaining: nonnegative(item.Remaining)}
		if window.Limit != nil && *window.Limit > 0 && window.Remaining != nil && *window.Remaining <= *window.Limit {
			percent := *window.Remaining / *window.Limit * 100
			window.RemainingPercent = &percent
		} else if item.Percentage != nil && *item.Percentage >= 0 && *item.Percentage <= 100 {
			percent := 100 - *item.Percentage
			window.RemainingPercent = &percent
		}
		if item.Reset > 0 {
			reset := time.UnixMilli(item.Reset).UTC()
			window.ResetAt = &reset
		}
		plan.Windows[name] = window
	}
	return plan, nil
}

func nonnegative(value *float64) *float64 {
	if value == nil || *value < 0 {
		return nil
	}
	return value
}
