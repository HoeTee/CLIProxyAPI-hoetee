package main

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type pointRates struct {
	CacheMiss float64 `yaml:"cache-miss"`
	CacheHit  float64 `yaml:"cache-hit"`
	Output    float64 `yaml:"output"`
}

type pointPricing struct {
	Divisor           float64               `yaml:"divisor"`
	OffPeakMultiplier float64               `yaml:"off-peak-multiplier"`
	PeakHours         []int                 `yaml:"peak-hours"`
	Models            map[string]pointRates `yaml:"models"`
}

func (p *pointPricing) validate() error {
	if p == nil {
		return nil
	}
	if !positive(p.Divisor) {
		return fmt.Errorf("GLM pricing.divisor must be positive and finite")
	}
	if !positive(p.OffPeakMultiplier) || p.OffPeakMultiplier > 1 {
		return fmt.Errorf("GLM pricing.off-peak-multiplier must be greater than zero and at most one")
	}
	if len(p.PeakHours) != 2 || p.PeakHours[0] < 0 || p.PeakHours[1] > 24 || p.PeakHours[0] >= p.PeakHours[1] {
		return fmt.Errorf("GLM pricing.peak-hours must be [start, end] within 0–24")
	}
	if len(p.Models) == 0 {
		return fmt.Errorf("GLM pricing.models must contain at least one model")
	}
	for model, rate := range p.Models {
		if model == "" || model != strings.ToLower(strings.TrimSpace(model)) ||
			!positive(rate.CacheMiss) || !positive(rate.CacheHit) || !positive(rate.Output) {
			return fmt.Errorf("invalid GLM pricing.models entry %q", model)
		}
	}
	return nil
}

func positive(value float64) bool {
	return value > 0 && !math.IsInf(value, 0)
}

func (p *pointPricing) estimate(usage *Usage) *float64 {
	if p == nil || usage == nil || usage.Created <= 0 || (usage.Cached == nil && usage.Input > 0) {
		return nil
	}
	rates, ok := p.Models[strings.ToLower(strings.TrimSpace(usage.Model))]
	if !ok {
		return nil
	}
	var cached int64
	if usage.Cached != nil {
		cached = *usage.Cached
	}
	points := (float64(usage.Input-cached)*rates.CacheMiss + float64(cached)*rates.CacheHit + float64(usage.Output)*rates.Output) / p.Divisor
	local := time.Unix(usage.Created, 0).In(beijing)
	peak := local.Weekday() >= time.Monday && local.Weekday() <= time.Friday &&
		local.Hour() >= p.PeakHours[0] && local.Hour() < p.PeakHours[1]
	if !peak {
		points *= p.OffPeakMultiplier
	}
	if math.IsInf(points, 0) || math.IsNaN(points) {
		return nil
	}
	return &points
}
