package usage

import "time"

type TokenHour struct {
	Start      time.Time `json:"start"`
	CacheHit   int64     `json:"cache_hit_tokens"`
	CacheMiss  int64     `json:"cache_miss_tokens"`
	CacheWrite int64     `json:"cache_write_tokens,omitempty"`
	Output     int64     `json:"output_tokens"`
}

type TokenHours [24]TokenHour

func NewTokenHours(now time.Time) TokenHours {
	var hours TokenHours
	start := now.UTC().Truncate(time.Hour).Add(-23 * time.Hour)
	for i := range hours {
		hours[i].Start = start.Add(time.Duration(i) * time.Hour)
	}
	return hours
}

func (hours *TokenHours) Add(at time.Time, hit, miss, write, output int64) {
	if at.Before(hours[0].Start) || !at.Before(hours[23].Start.Add(time.Hour)) {
		return
	}
	hour := &hours[int(at.Sub(hours[0].Start)/time.Hour)]
	hour.CacheHit += hit
	hour.CacheMiss += miss
	hour.CacheWrite += write
	hour.Output += output
}
