package main

import "strings"

// modelRate holds the published credit price per 1M tokens for one model family.
// Values come from the official Codex pricing token-rate table.
type modelRate struct {
	// Match is a lowercase substring matched against the model identifier.
	Match string
	// Input is the credit price per 1M uncached input tokens.
	Input float64
	// Cached is the credit price per 1M cached input tokens.
	Cached float64
	// Output is the credit price per 1M output tokens.
	Output float64
}

// rateRow is the configuration override form for a model rate.
type rateRow struct {
	Input  float64 `yaml:"input"`
	Cached float64 `yaml:"cached"`
	Output float64 `yaml:"output"`
}

// defaultRate is used when no rate rule matches the model identifier.
var defaultRate = modelRate{Match: "default", Input: 100, Cached: 10, Output: 500}

// builtinRates is ordered: more specific patterns must appear before broader ones.
var builtinRates = []modelRate{
	{Match: "astra", Input: 250, Cached: 25, Output: 1250},
	{Match: "5.4 mini", Input: 18.75, Cached: 1.875, Output: 113},
	{Match: "5.4-mini", Input: 18.75, Cached: 1.875, Output: 113},
	{Match: "daybreak red", Input: 312.5, Cached: 31.25, Output: 1875},
	{Match: "daybreak", Input: 100, Cached: 10, Output: 500},
	{Match: "sol", Input: 100, Cached: 10, Output: 500},
	{Match: "terra", Input: 50, Cached: 5, Output: 300},
	{Match: "luna", Input: 5, Cached: 0.5, Output: 30},
	{Match: "rosalind", Input: 125, Cached: 12.5, Output: 625},
	{Match: "5.5", Input: 125, Cached: 12.5, Output: 750},
	{Match: "5.4", Input: 62.5, Cached: 6.25, Output: 375},
}

// rateResolver resolves model identifiers to credit rates.
type rateResolver struct {
	overrides []modelRate
}

// newRateResolver builds a resolver from configured overrides.
func newRateResolver(overrides map[string]rateRow) *rateResolver {
	resolver := &rateResolver{}
	for name, row := range overrides {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		resolver.overrides = append(resolver.overrides, modelRate{
			Match:  key,
			Input:  row.Input,
			Cached: row.Cached,
			Output: row.Output,
		})
	}
	return resolver
}

// resolve returns the rate for a model identifier. Overrides win over builtins.
func (r *rateResolver) resolve(model string) modelRate {
	if r != nil {
		for _, rule := range r.overrides {
			if strings.Contains(model, rule.Match) {
				return rule
			}
		}
	}
	for _, rule := range builtinRates {
		if strings.Contains(model, rule.Match) {
			return rule
		}
	}
	return defaultRate
}

// unitCost converts token counters into credits.
//
// Cached input is treated as a discounted subset of input, matching the shape of
// the published rate table where the cached price is exactly 0.1x the input
// price. CacheRead is preferred over Cached because the OpenAI execution path
// assigns both fields the same value.
func (r modelRate) unitCost(tokens tokenSum) float64 {
	cached := tokens.CacheRead
	if cached == 0 {
		cached = tokens.Cached
	}
	uncached := tokens.Input - cached
	if uncached < 0 {
		uncached = 0
	}
	total := float64(uncached)*r.Input +
		float64(cached)*r.Cached +
		float64(tokens.CacheCreate)*r.Input +
		float64(tokens.Output)*r.Output
	return total / 1e6
}
