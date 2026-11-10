package glmquota

import (
	"math"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

const ProviderName = "glm-coding-plan"
const ProviderKey = "openai-compatible-" + ProviderName
const PluginID = "glm-key-quota"

type Usage = pluginapi.ClientQuotaUsage

// ParseUsage reads the original upstream response before protocol translation.
func ParseUsage(body []byte) *Usage {
	var response ResponseUsage
	response.Observe(body)
	return response.Result()
}

// ResponseUsage retains upstream metadata across chunks until final usage arrives.
type ResponseUsage struct {
	usage   *Usage
	model   string
	created int64
}

func (r *ResponseUsage) Observe(body []byte) {
	root := gjson.ParseBytes(body)
	if model := root.Get("model"); model.Type == gjson.String && model.Str != "" {
		r.model = model.Str
	}
	if created := root.Get("created"); tokenCount(created) && created.Int() > 0 {
		r.created = created.Int()
	}
	node := root.Get("usage")
	input, output := node.Get("prompt_tokens"), node.Get("completion_tokens")
	if !tokenCount(input) || !tokenCount(output) || input.Int() > math.MaxInt64-output.Int() {
		return
	}
	usage := &Usage{Input: input.Int(), Output: output.Int()}
	if cached := node.Get("prompt_tokens_details.cached_tokens"); tokenCount(cached) && cached.Int() <= usage.Input {
		value := cached.Int()
		usage.Cached = &value
	}
	r.usage = usage
}

func (r *ResponseUsage) Result() *Usage {
	if r.usage == nil {
		return nil
	}
	result := *r.usage
	result.Model = r.model
	result.Created = r.created
	return &result
}

func tokenCount(value gjson.Result) bool {
	return value.Type == gjson.Number && value.Num >= 0 && value.Num < float64(math.MaxInt64) && value.Num == math.Trunc(value.Num)
}

type Window struct {
	Used            int64     `json:"used_tokens"`
	Unknown         int64     `json:"unknown_requests"`
	ResetAt         time.Time `json:"reset_at,omitzero"`
	EstimatedPoints *float64  `json:"estimated_points,omitempty"`
	Unpriced        int64     `json:"unpriced_requests,omitempty"`
}

type Hour struct {
	Start        time.Time `json:"start"`
	CacheHit     int64     `json:"cache_hit_tokens"`
	CacheMiss    int64     `json:"cache_miss_tokens"`
	CacheUnknown int64     `json:"cache_unknown_tokens"`
	Output       int64     `json:"output_tokens"`
	Unknown      int64     `json:"unknown_requests"`
}

type Snapshot struct {
	FiveHour Window `json:"five_hour"`
	Weekly   Window `json:"weekly"`
	Hours    []Hour `json:"hours"`
}

type QuotaError struct {
	Message string
	Status  int
}

func (e QuotaError) Error() string       { return e.Message }
func (e QuotaError) StatusCode() int     { return e.Status }
func (e QuotaError) IsRequestStop() bool { return true }
