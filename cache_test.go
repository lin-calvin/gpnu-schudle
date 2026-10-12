package main

import (
	"testing"
	"time"
)

func TestCacheExpiry(t *testing.T) {
	at := func(h, m int) time.Time {
		return time.Date(2026, 10, 12, h, m, 0, 0, cst)
	}
	// 09:55 -> 12h 后 (=21:55)，早于次日 00:00
	if got := cacheExpiry(at(9, 55)); !got.Equal(at(21, 55)) {
		t.Fatalf("09:55 expect 21:55, got %s", got)
	}
	// 20:00 -> 次日 00:00 (4h)，早于 12h
	want := time.Date(2026, 10, 13, 0, 0, 0, 0, cst)
	if got := cacheExpiry(at(20, 0)); !got.Equal(want) {
		t.Fatalf("20:00 expect next midnight, got %s", got)
	}
	// 01:00 -> min(13:00, 次日00:00) = 13:00
	if got := cacheExpiry(at(1, 0)); !got.Equal(at(13, 0)) {
		t.Fatalf("01:00 expect 13:00, got %s", got)
	}
}

func TestCacheGetPut(t *testing.T) {
	cachePut("k1", &scheduleData{xn: "2026", xq: 1})
	if d, ok := cacheGet("k1"); !ok || d.xn != "2026" {
		t.Fatal("expected cache hit")
	}
	// 过期后 miss
	scheduleCache.Lock()
	e := scheduleCache.m["k1"]
	e.expiry = nowCST().Add(-time.Hour)
	scheduleCache.m["k1"] = e
	scheduleCache.Unlock()
	if _, ok := cacheGet("k1"); ok {
		t.Fatal("expected expired miss")
	}
}
