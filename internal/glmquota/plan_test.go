package glmquota

import (
	"strings"
	"testing"
	"time"
)

func TestParseDomesticCreditWindows(t *testing.T) {
	plan, err := parsePlan(strings.NewReader(`{"code":200,"success":true,"data":{"level":"max","limits":[
		{"type":"CREDIT_LIMIT","unit":6,"number":1,"percentage":3},
		{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":28000,"currentValue":7000,"remaining":21000,"percentage":25,"nextResetTime":1794200000000},
		{"type":"TIME_LIMIT","unit":6,"number":1,"percentage":70}]}}`))
	if err != nil || len(plan.Windows) != 2 {
		t.Fatalf("plan = %+v, error = %v", plan, err)
	}
	five, week := plan.Windows["five_hour"], plan.Windows["weekly"]
	if five.Remaining == nil || *five.Remaining != 21000 || five.RemainingPercent == nil ||
		*five.RemainingPercent != 75 || five.ResetAt == nil ||
		!five.ResetAt.Equal(time.UnixMilli(1794200000000)) {
		t.Fatalf("five-hour window = %+v", five)
	}
	if week.Remaining != nil || week.RemainingPercent == nil || *week.RemainingPercent != 97 {
		t.Fatalf("weekly percentage-only window = %+v", week)
	}
}
