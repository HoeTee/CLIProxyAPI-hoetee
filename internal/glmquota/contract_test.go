package glmquota

import "testing"

func TestResponseUsageRetainsMetadataFromEarlierChunk(t *testing.T) {
	var response ResponseUsage
	response.Observe([]byte(`{"model":"glm-5.3-flash","created":1789641381,"usage":null}`))
	response.Observe([]byte(`{"usage":{"prompt_tokens":1075,"completion_tokens":241,"prompt_tokens_details":{"cached_tokens":1024}}}`))
	got := response.Result()
	if got == nil || got.Model != "glm-5.3-flash" || got.Created != 1789641381 ||
		got.Input != 1075 || got.Output != 241 || got.Cached == nil || *got.Cached != 1024 {
		t.Fatalf("upstream usage = %+v", got)
	}
}

func TestParseUsageDistinguishesMissingAndZeroCache(t *testing.T) {
	absent := ParseUsage([]byte(`{"usage":{"prompt_tokens":9,"completion_tokens":1}}`))
	zero := ParseUsage([]byte(`{"usage":{"prompt_tokens":9,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":0}}}`))
	if absent == nil || absent.Cached != nil || zero == nil || zero.Cached == nil || *zero.Cached != 0 {
		t.Fatalf("missing cache = %+v, zero cache = %+v", absent, zero)
	}
	if got := ParseUsage([]byte(`{"usage":{"total_tokens":0}}`)); got != nil {
		t.Fatalf("incomplete usage = %+v", got)
	}
}
