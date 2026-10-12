package main

import (
	"strconv"
	"sync"
	"time"
)

// scheduleData 是渲染 今日/整周/ICS 所需的全部数据（不含"当前周"，周次实时算）
type scheduleData struct {
	xn        string
	xq        int
	xqm       string
	weekCount int
	zs        string // 门户返回的"当前周"，仅作兜底
	xqj       int    // 门户返回的"当前星期几"，仅作兜底
	kb        *kbResp
	times     map[int][2]string
}

type scheduleEntry struct {
	data     *scheduleData
	expiry   time.Time
	lastUsed time.Time
}

var scheduleCache = struct {
	sync.Mutex
	m map[string]scheduleEntry
}{m: map[string]scheduleEntry{}}

const scheduleCacheMax = 200

// cacheExpiry 取「now+12h」与「次日 00:00 (CST)」中较早者：
// 既保证最多缓存 12 小时，又保证每天 00:00 刷新一次。
func cacheExpiry(now time.Time) time.Time {
	local := now.In(cst)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, cst).AddDate(0, 0, 1)
	twelve := local.Add(12 * time.Hour)
	if midnight.Before(twelve) {
		return midnight
	}
	return twelve
}

func cacheGet(key string) (*scheduleData, bool) {
	scheduleCache.Lock()
	defer scheduleCache.Unlock()
	entry, ok := scheduleCache.m[key]
	if !ok {
		return nil, false
	}
	if nowCST().After(entry.expiry) {
		delete(scheduleCache.m, key)
		return nil, false
	}
	entry.lastUsed = time.Now()
	scheduleCache.m[key] = entry
	return entry.data, true
}

func cachePut(key string, data *scheduleData) {
	now := time.Now()
	scheduleCache.Lock()
	defer scheduleCache.Unlock()
	scheduleCache.m[key] = scheduleEntry{data: data, expiry: cacheExpiry(now), lastUsed: now}
	evictSchedulesLocked(now)
}

func cacheInvalidate(key string) {
	scheduleCache.Lock()
	delete(scheduleCache.m, key)
	scheduleCache.Unlock()
}

func evictSchedulesLocked(now time.Time) {
	if len(scheduleCache.m) <= scheduleCacheMax {
		return
	}
	for key, entry := range scheduleCache.m {
		if nowCST().After(entry.expiry) {
			delete(scheduleCache.m, key)
		}
	}
	for len(scheduleCache.m) > scheduleCacheMax {
		var oldestKey string
		var oldest time.Time
		first := true
		for key, entry := range scheduleCache.m {
			if first || entry.lastUsed.Before(oldest) {
				oldest = entry.lastUsed
				oldestKey = key
				first = false
			}
		}
		delete(scheduleCache.m, oldestKey)
	}
}

// fetchSchedule 拉取门户周次信息 + 教务课表 + 节次时间
func fetchSchedule(keys *keysFile) (*scheduleData, error) {
	pr, err := getPortalWeek(keys)
	if err != nil {
		return nil, err
	}
	xq, _ := strconv.Atoi(pr.Data.XQ)
	if xq < 1 {
		xq = 1
	}
	xqm := xqmOf(xq)
	kb, err := getSchedule(keys, pr.Data.XN, xqm)
	if err != nil {
		return nil, err
	}
	times, _ := getTimes(keys, pr.Data.XN, xqm)
	weekCount, _ := strconv.Atoi(pr.Data.WeekCount)
	if weekCount < 1 {
		weekCount = 18
	}
	xqj, _ := strconv.Atoi(pr.Data.XQJ)
	if xqj < 1 || xqj > 7 {
		xqj = 1
	}
	return &scheduleData{
		xn: pr.Data.XN, xq: xq, xqm: xqm, weekCount: weekCount,
		zs: pr.Data.ZS, xqj: xqj, kb: kb, times: times,
	}, nil
}

// scheduleForEnv 环境变量单用户的课表数据（12h/午夜缓存，命中即免登录）
func scheduleForEnv(keysPath string) (*scheduleData, error) {
	const key = "env"
	if data, ok := cacheGet(key); ok {
		return data, nil
	}

	var data *scheduleData
	_, err := withKeysRetry(keysPath, func(keys *keysFile) (any, error) {
		fetched, err := fetchSchedule(keys)
		if err != nil {
			return nil, err
		}
		data = fetched
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	cachePut(key, data)
	return data, nil
}
