package server

import (
	"net/http/httptest"
	"testing"
)

func TestSecurityPaginationLimitOneRemainsPositive(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/groups?page=9223372036854775807&limit=1", nil)
	p, l, _ := parsePagination(r)
	if p < 1 || (p-1)*l < 0 {
		t.Fatal("limit=1 produced invalid page or offset")
	}
	if len(paginate([]string{"synthetic"}, p, l)["items"].([]string)) != 0 {
		t.Fatal("huge page not empty")
	}
}

func TestSecurityPaginationOverflowReturnsEmpty(t *testing.T) {
	for _, raw := range []string{"9223372036854775807", "999999999999999999999999999999999999"} {
		r := httptest.NewRequest("GET", "/api/groups?page="+raw+"&limit=500", nil)
		p, l, _ := parsePagination(r)
		if (p-1)*l < 0 {
			t.Fatal("SQL offset overflows")
		}
		result := paginate([]string{"synthetic"}, p, l)
		if len(result["items"].([]string)) != 0 {
			t.Fatal("huge page did not return empty")
		}
	}
	maxInt := int(^uint(0) >> 1)
	result := paginate([]string{"synthetic"}, maxInt, maxInt)
	if len(result["items"].([]string)) != 0 {
		t.Fatal("direct pagination not empty")
	}
}
